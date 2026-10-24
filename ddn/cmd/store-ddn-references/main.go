// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Command store-ddn-references serves the Hasura DDN engine plugin of
// package ddnrefs: POST /pre-parse checks the references a mutation writes and
// the referrers of what it deletes; POST /pre-response publishes the NOTIFY
// events of deletes that happened.
//
//	store-ddn-references -config ddn/references.json -listen :8787
//
// Environment: STORE_REFERENCES_PLUGIN_SECRET (the hasura-m-auth value the
// engine sends), DDN_URL and DDN_ADMIN_SECRET (to read the name of a nested
// resource a delete names by key), CLOUDEVENTS_SINK and CLOUDEVENTS_SOURCE
// (NOTIFY events), and whatever ${NAME} the config's endpoint targets name.
// gRPC connections use TLS with the system roots unless -insecure.
package main

import (
	"crypto/tls"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/the-protobuf-project/store/ddn/ddnrefs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	config := flag.String("config", "references.json", "the ddn/references.json protoc-gen-store emitted")
	listen := flag.String("listen", ":8787", "address to serve the plugin on")
	plain := flag.Bool("insecure", false, "dial gRPC endpoints without TLS")
	flag.Parse()

	cfg, err := ddnrefs.Load(*config)
	if err != nil {
		log.Fatal(err)
	}
	creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	if *plain {
		creds = insecure.NewCredentials()
	}
	headers := map[string]string{}
	if s := os.Getenv("DDN_ADMIN_SECRET"); s != "" {
		headers["x-hasura-admin-secret"] = s
	}
	p, err := ddnrefs.New(cfg, ddnrefs.Options{
		Dial: func(target string) (grpc.ClientConnInterface, error) {
			return grpc.NewClient(target, grpc.WithTransportCredentials(creds))
		},
		DDNURL:     os.Getenv("DDN_URL"),
		DDNHeaders: headers,
		Secret:     os.Getenv("STORE_REFERENCES_PLUGIN_SECRET"),
		Sink:       os.Getenv("CLOUDEVENTS_SINK"),
		Source:     os.Getenv("CLOUDEVENTS_SOURCE"),
		Getenv:     os.Getenv,
	})
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("POST /pre-parse", p.PreParse())
	mux.Handle("POST /pre-response", p.PreResponse())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	slog.Info("store-ddn-references listening", "addr", *listen, "mutations", len(cfg.Mutations))
	log.Fatal(http.ListenAndServe(*listen, mux))
}
