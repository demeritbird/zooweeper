package zab

import (
	"encoding/json"
	"github.com/fatih/color"
	"net/http"
	"strings"
	"time"
)

// ProposalOps for 2PC of Write Request (Ref: Active Messaging in https://zookeeper.apache.org/doc/current/zookeeperInternals.html)
type ProposalOps struct {
	ab *AtomicBroadcast
}

// ProposeWrite handler on Follower nodes to ACK upon receive
func (po *ProposalOps) ProposeWrite(w http.ResponseWriter, r *http.Request) {
	zNode, _ := po.ab.ZTree.GetLocalMetadata()
	clientPort := r.Header.Get("X-Sender-Port")
	if clientPort != zNode.Leader {
		color.Red("I only supposed to receive Propose Write from leader not from %s\n", clientPort)
	}

	data := po.ab.CreateMetadataFromPayload(w, r)
	jsonData, _ := json.Marshal(data)

	color.HiBlue("%s received Propose Write from %s\n", zNode.NodePort, clientPort)
	color.HiBlue("%s sending proposalACK to %s\n", zNode.NodePort, clientPort)
	url := po.ab.BaseURL + ":" + zNode.Leader + "/acknowledgeProposal"
	_, err = po.ab.sendRequest(url, "POST", jsonData)
}

// AcknowledgeProposal handler on Leader node to wait for majority ACK before commit
func (po *ProposalOps) AcknowledgeProposal(w http.ResponseWriter, r *http.Request) {
	zNode, _ := po.ab.ZTree.GetLocalMetadata()
	clientPort := r.Header.Get("X-Sender-Port")
	if clientPort == zNode.Leader {
		color.Red("I'm the Leader I'm not supposed to get acknowledged from myself\n")
	}
	color.HiBlue("Leader %s received ACK from Follower %s\n", zNode.NodePort, clientPort)

	// Wait for majority of Follower to ACK
	portsSlice := strings.Split(zNode.Servers, ",")
	majority := len(portsSlice) / 2

	if po.ab.ProposalState() != ACKNOWLEDGED {
		for {
			currentAckCount := po.ab.AckCounter()
			currentAckCount++
			po.ab.SetAckCounter(currentAckCount)

			if currentAckCount > majority {
				color.HiBlue("Leader %s received majority proposalAck, %d\n", zNode.NodePort, currentAckCount)
				po.ab.SetAckCounter(0)
				po.ab.SetProposalState(ACKNOWLEDGED)
				break
			} else if po.ab.ProposalState() == ACKNOWLEDGED {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	data := po.ab.CreateMetadataFromPayload(w, r)
	jsonData, _ := json.Marshal(data)

	color.HiBlue("Leader %s asking Follower %s to commit\n", zNode.NodePort, clientPort)
	url := po.ab.BaseURL + ":" + clientPort + "/commitWrite"
	_, err = po.ab.sendRequest(url, "POST", jsonData)
	if err != nil {
		color.HiBlue("Error Asking Follower %s to commit: %s\n", clientPort, err.Error())
	}

}

// CommitWrite handler on Follower nodes to commit upon receive
func (po *ProposalOps) CommitWrite(w http.ResponseWriter, r *http.Request) {
	zNode, _ := po.ab.ZTree.GetLocalMetadata()
	clientPort := r.Header.Get("X-Sender-Port")

	data := po.ab.CreateMetadataFromPayload(w, r)
	jsonData, _ := json.Marshal(data)

	color.HiBlue("%s receive Commit Write from %s\n", zNode.NodePort, clientPort)
	color.HiBlue("%s Committing Write\n", zNode.NodePort)
	url := po.ab.BaseURL + ":" + zNode.NodePort + "/writeMetadata"
	_, err = po.ab.sendRequest(url, "POST", jsonData)
	if err != nil {
		color.Red("Error Commiting Write: %s\n", err.Error())
	}
}
