package main

import (
	"flag"
	"html/template"
	"ljightningparking/handlers"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	logPath := flag.String("logpath", "", "log path")
	listenAddress := flag.String("listen", ":8080", "listen address")
	staticPath := flag.String("static", "", "static path")
	//lndAddr := flag.String("lnd", "", "lnd address for generating lnd invoice")
	//macaroonPath := flag.String("macaroon", "", "path to the invoice macaroon file")
	templatePath := flag.String("template", "", "template path")

	flag.Parse()

	if len(*logPath) > 0 {
		f, err := os.OpenFile(*logPath+"ljightningparking.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			log.Fatalf("error opening file: %v", err)
		}
		defer f.Close()

		log.SetOutput(f)
	}

	templateFiles, err := filepath.Glob(*templatePath)
	if err != nil {
		log.Fatalf("error listing template files: %s", err)
	}

	handlers.BaseTemplate = template.Must(template.ParseFiles(templateFiles...))

	//lnd.InitHandler(*lndAddr, *macaroonPath)

	http.HandleFunc("/", handlers.MainHandler)
	http.HandleFunc("/pay", handlers.PayHandler)
	http.HandleFunc("/check", handlers.CheckHandler)

	fs := http.FileServer(http.Dir(*staticPath))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
