package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"pocketful/app"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{
		Addr:              "0.0.0.0:" + port,
		Handler:           app.New(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("pocketful listening on %s", srv.Addr)
	log.Fatal(srv.ListenAndServe())
}
