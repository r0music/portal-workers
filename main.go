package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/incognitochain/portal-workers/utils"
	"github.com/joho/godotenv"
)

func main() {
	var envFile, workersStr string
	flag.StringVar(&envFile, "config", ".env_dev", ".env config file")
	flag.StringVar(&workersStr, "workers", "1,2,3", "Excuted worker IDs")
	flag.Parse()

	workerIDsStr := strings.Split(workersStr, ",")
	workerIDs := []int{}
	for _, str := range workerIDsStr {
		workerIDInt, err := strconv.Atoi(str)
		if err != nil {
			panic("Worker ID is invalid")
		}
		workerIDs = append(workerIDs, workerIDInt)
	}
	fmt.Printf("List of executed worker IDs: %+v\n", workerIDs)

	err := godotenv.Load(envFile)
	if err != nil {
		panic(fmt.Sprintf("Error loading %v file", envFile))
	}

	var myEnv map[string]string
	myEnv, _ = godotenv.Read(envFile)
	fmt.Println("=========Config============")
	for key, value := range myEnv {
		fmt.Println(key + ": " + value)
	}
	fmt.Println("=========End============")

	runtime.GOMAXPROCS(runtime.NumCPU())
	s := NewServer(workerIDs)

	// split utxos before executing workers
	if os.Getenv("SPLITUTXO") == "true" {
		privateKey := os.Getenv("INCOGNITO_PRIVATE_KEY")
		paymentAddress := os.Getenv("INCOGNITO_PAYMENT_ADDRESS")
		minNumUTXOsStr := os.Getenv("NUMUTXO")
		minNumUTXOs, _ := strconv.Atoi(minNumUTXOsStr)
		protocol := os.Getenv("INCOGNITO_PROTOCOL")
		endpointUri := fmt.Sprintf("%v://%v:%v", os.Getenv("INCOGNITO_PROTOCOL"), os.Getenv("INCOGNITO_HOST"), os.Getenv("INCOGNITO_PORT"))

		err := utils.SplitUTXOs(endpointUri, protocol, privateKey, paymentAddress, minNumUTXOs)
		if err != nil {
			panic("Could not split UTXOs")
		}
	}

	s.Run()
	for range s.workers {
		<-s.finish
	}
	fmt.Println("Server stopped gracefully!")
}
