// ReadOps and WriteOps have Self-reference to parent - ref: https://stackoverflow.com/questions/27918208/go-get-parent-struct

package zooweeper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/fatih/color"
	"github.com/tnbl265/zooweeper/request_processors/data"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tnbl265/zooweeper/ztree"
)

type ProposalState string

const (
	PROPOSED     ProposalState = "PROPOSED"
	ACKNOWLEDGED ProposalState = "ACKNOWLEDGED"
	COMMITTED    ProposalState = "COMMITTED"
)

type SyncState string

const (
	PREPARED SyncState = "PREPARED"
	ACKED    SyncState = "ACKED"
	SYNCED   SyncState = "SYNCED"
)

var err error

type AtomicBroadcast struct {
	BaseURL string

	Read     ReadOps
	Write    WriteOps
	Proposal ProposalOps
	Election ElectionOps
	Sync     SyncOps

	ZTree ztree.ZNodeHandlers

	// Proposal
	ackCounter    int
	proposalState ProposalState
	proposalMu    sync.Mutex

	// DataSync
	syncCounter int
	syncState   SyncState
	syncMu      sync.Mutex

	pq PriorityQueue

	ErrorLeaderChan chan data.HealthCheckError
}

func (ab *AtomicBroadcast) forwardRequestToLeader(r *http.Request) (*http.Response, error) {
	zNode, _ := ab.ZTree.GetLocalMetadata()
	req, _ := http.NewRequest(r.Method, ab.BaseURL+":"+zNode.Leader+r.URL.Path, r.Body)
	req.Header = r.Header
	client := &http.Client{}
	return client.Do(req)
}

func (ab *AtomicBroadcast) startProposal(data data.Data) {
	ab.SetProposalState(PROPOSED)

	jsonData, _ := json.Marshal(data)
	zNode, _ := ab.ZTree.GetLocalMetadata()
	portsSlice := strings.Split(zNode.Servers, ",")

	// send Request async
	var wg sync.WaitGroup
	for _, port := range portsSlice {
		if port == zNode.NodePort {
			continue
		}

		wg.Add(1)
		go func(port string) {
			defer wg.Done()

			color.HiBlue("Leader %s proposing to Follower %s", zNode.NodePort, port)
			url := ab.BaseURL + ":" + port + "/proposeWrite"
			_, err := ab.makeExternalRequest(nil, url, "POST", jsonData)
			if err != nil {
				color.Red("Error proposing to follower:", port, "Error:", err)
			}
		}(port)
	}
	wg.Wait()

	// Wait for ACK before committing
	for ab.ProposalState() != ACKNOWLEDGED {
		time.Sleep(time.Second)
	}

	color.HiBlue("Leader %s committing", zNode.NodePort)
	url := ab.BaseURL + ":" + zNode.NodePort + "/writeMetadata"
	_, err := ab.makeExternalRequest(nil, url, "POST", jsonData)
	if err != nil {
		color.Red("Error committing write metadata:", err)
	}
	ab.SetProposalState(COMMITTED)
}

// syncMetadata for new leader to sync its transaction log
func (ab *AtomicBroadcast) syncMetadata() {
	ab.SetSyncState(PREPARED)

	zNode, _ := ab.ZTree.GetLocalMetadata()
	portsSlice := strings.Split(zNode.Servers, ",")

	var metadata ztree.Metadata
	jsonData, _ := json.Marshal(metadata)

	// send Request async
	var wg sync.WaitGroup
	for _, port := range portsSlice {
		if port == zNode.NodePort {
			continue
		}

		wg.Add(1)
		go func(port string) {
			defer wg.Done()

			color.Yellow("%s send syncRequest to %s", zNode.NodePort, port)
			url := ab.BaseURL + ":" + port + "/syncRequest"
			_, err := ab.makeExternalRequest(nil, url, "POST", jsonData)
			if err != nil {
				color.Red("Error syncRequest to %s:", port, "Error:", err)
			}
		}(port)
	}
	wg.Wait()

	// Wait for ACK before request Metadata
	for ab.SyncState() != ACKED {
		time.Sleep(time.Second)
	}

	highestZNodeId, _ := ab.ZTree.GetHighestZNodeId()
	metadata = ztree.Metadata{
		NodeId: highestZNodeId,
	}
	jsonData, _ = json.Marshal(metadata)

	for _, port := range portsSlice {
		if port == zNode.NodePort {
			continue
		}

		color.Yellow("%s requestMetadata from %s", zNode.NodePort, port)
		url := ab.BaseURL + ":" + port + "/requestMetadata"
		_, err := ab.makeExternalRequest(nil, url, "POST", jsonData)
		if err != nil {
			color.Red("Error requestMetadata to:", port, "Error:", err)
		}
	}

	color.Yellow("%s finished syncing", zNode.NodePort)
	ab.SetSyncState(SYNCED)
}

func (ab *AtomicBroadcast) WakeupLeaderElection(port int) {
	for {
		select {
		case <-time.After(time.Second * 2):
			// On wake-up, start leader-election
			data := data.HealthCheckError{
				Error:     nil,
				ErrorPort: fmt.Sprintf("%d", port),
				IsWakeup:  true,
			}
			ab.ErrorLeaderChan <- data
			return
		}
	}
}
func (ab *AtomicBroadcast) ListenForLeaderElection(port int, leader int) {
	for {
		select {
		case errorData := <-ab.ErrorLeaderChan:
			errorPortNumber, _ := strconv.Atoi(errorData.ErrorPort)
			if errorPortNumber == leader || errorData.IsWakeup {
				if errorData.IsWakeup {
					color.Cyan("%d joining, starting election", port)
				} else if errorPortNumber == leader {
					color.Cyan("Healthcheck timeout for %d, starting election", errorPortNumber)
				}
				ab.startLeaderElection(port)
			}
		}
	}
}

// Starts the leader election by calling its own server handler with information of the port information.
func (ab *AtomicBroadcast) startLeaderElection(currentPort int) {
	client := &http.Client{}
	portURL := fmt.Sprintf("%d", currentPort)

	url := fmt.Sprintf(ab.BaseURL + ":" + portURL + "/electLeader")
	var electMessage = data.ElectLeaderRequest{
		IncomingPort: fmt.Sprintf("%d", currentPort),
	}
	jsonData, _ := json.Marshal(electMessage)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		//log.Println(err)
	}
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
	}
	defer resp.Body.Close()
}

// Send request to all other nodes that outgoing port is a leader.
func (ab *AtomicBroadcast) declareLeaderRequest(portStr string, allServers []string) {
	for _, outgoingPort := range allServers {
		//make a request
		client := &http.Client{}
		portURL := fmt.Sprintf("%s", outgoingPort)

		url := fmt.Sprintf(ab.BaseURL + ":" + portURL + "/declareLeaderReceive")
		var electMessage = data.DeclareLeaderRequest{
			IncomingPort: portStr,
		}
		jsonData, _ := json.Marshal(electMessage)

		req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			//log.Println(err)
			continue
		}
		req.Header.Add("Accept", "application/json")
		req.Header.Add("Content-Type", "application/json")

		color.Cyan("%s declare Leader to %s", portStr, outgoingPort)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()
	}
}
