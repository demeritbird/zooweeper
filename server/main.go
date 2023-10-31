package main

import (
	"database/sql"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	ensemble "github.com/tnbl265/zooweeper/ensemble"
	"log"
	"net/http"
	"os"
	"strconv"
)

func main() {
	portStr := os.Getenv("PORT")
	if portStr == "" {
		portStr = "8080"
	}
	port, _ := strconv.Atoi(portStr)

	var state ensemble.ServerState
	leader := 8080
	allServers := []int{8080, 8081, 8082}

	var dbPath string
	switch port {
	case 8080:
		dbPath = "database/zooweeper-metadata-0.db"
		state = ensemble.LEADING
	case 8081:
		dbPath = "database/zooweeper-metadata-1.db"
		state = ensemble.FOLLOWING
	case 8082:
		dbPath = "database/zooweeper-metadata-2.db"
		state = ensemble.FOLLOWING
	default:
		log.Fatalf("Only support ports 8080, 8081 or 8082")
	}

	// Start Server
	server := ensemble.NewServer(port, leader, state, allServers, dbPath)
	log.Printf("Starting Server (%s) on port %s\n", server.State(), portStr)
	defer func(connection *sql.DB) {
		err := connection.Close()
		if err != nil {
			log.Fatal(err)
		}
	}(server.Rp.Zab.ZTree.Connection())

	err := http.ListenAndServe(fmt.Sprintf(":"+portStr), server.Rp.Routes())
	if err != nil {
		log.Fatal(err)
	}
}
