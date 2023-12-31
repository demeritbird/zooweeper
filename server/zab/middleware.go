package zab

import (
	"bytes"
	"container/heap"
	"encoding/json"
	"github.com/fatih/color"
	"github.com/tnbl265/zooweeper/request_processors/data"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"time"
)

// RequestItem with Timestamp field for ordering in PriorityQueue
type RequestItem struct {
	Request   *http.Request
	Timestamp string
}

// PriorityQueue of RequestItem to be used by QueueMiddleware
type PriorityQueue []*RequestItem

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	return pq[i].Timestamp < pq[j].Timestamp
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}

func (pq *PriorityQueue) Push(x interface{}) {
	item := x.(*RequestItem)
	*pq = append(*pq, item)
}

func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}

func (pq *PriorityQueue) Peek() *RequestItem {
	if len(*pq) == 0 {
		return nil
	}
	return (*pq)[0]
}

// QueueMiddleware to order RequestItem using PriorityQueue
func (ab *AtomicBroadcast) QueueMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract the timestamp
		timestamp, _ := func(r *http.Request) (string, error) {
			// Read the request body
			body, err := ioutil.ReadAll(r.Body)
			if err != nil {
				return "", err
			}

			// Decode the body into the struct
			var data data.Data
			err = json.Unmarshal(body, &data)
			if err != nil {
				return "", err
			}

			// Close and replace the original body so it can be read again if necessary
			r.Body.Close()
			r.Body = ioutil.NopCloser(bytes.NewBuffer(body))

			// Return the timestamp
			return data.Timestamp, nil
		}(r)

		// Use minPriorityQueue to ensure FIFO Client Order
		item := &RequestItem{
			Request:   r,
			Timestamp: timestamp,
		}
		heap.Push(&ab.pq, item)
		for ab.pq.Peek() != item {
			time.Sleep(time.Second)
		}
		next.ServeHTTP(w, r)
		heap.Pop(&ab.pq)
	})
}

// WriteOpsMiddleware to establish some form of Total Order for RequestItem using PriorityQueue
func (wo *WriteOps) WriteOpsMiddleware(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zNode, err := wo.ab.ZTree.GetLocalMetadata()
		if err != nil {
			log.Println("WriteOpsMiddleware Error:", err)
			return
		}

		if zNode.NodePort != zNode.Leader {
			// Follower will forward Request to Leader
			color.HiBlue("%s forwarding request to leader %s", zNode.NodePort, zNode.Leader)
			resp, err := wo.ab.forwardRequestToLeader(r)
			if err != nil {
				// Handle error
				http.Error(w, "Failed to forward request", http.StatusInternalServerError)
				return
			}

			// Copy Header and Status code
			for name, values := range resp.Header {
				for _, value := range values {
					w.Header().Add(name, value)
				}
			}
			w.WriteHeader(resp.StatusCode)

			io.Copy(w, resp.Body)
			return
		} else {
			// Leader will Propose, wait for Acknowledge, before Commit
			data := wo.ab.CreateMetadataFromPayload(w, r)
			for wo.ab.ProposalState() != COMMITTED {
				// Propose in sequence to ensure Linearization Write
				time.Sleep(time.Second)
			}
			wo.ab.startProposal(data)
			return
		}
	})
}
