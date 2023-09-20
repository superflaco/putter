package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type requestRecord struct {
	Timestamp   time.Time
	Method      string
	Uri         string
	PayloadSize int
	PayloadHash string
	Payload     string
}

func (r requestRecord) String() string {
	return r.Timestamp.Format(time.RFC3339) + " " + r.Method + " " + r.Uri + " " + strconv.Itoa(r.PayloadSize) + " " + r.PayloadHash + "\n\t" + r.Payload + "\n--\n"
}

var port, callCount, headerLimit, goroutinelimit int
var bufferRequest, storePayload bool
var recordedCalls []requestRecord
var callChan chan requestRecord
var delay, variance, chance int
var random *rand.Rand

func init() {
	flag.IntVar(&port, "p", 7758, "Listen Port")
	flag.IntVar(&callCount, "c", 100, "Count of Calls to Record")
	flag.IntVar(&headerLimit, "h", 1, "Header Size Limit in MB")
	flag.BoolVar(&bufferRequest, "b", false, "Fully Buffer Input Before Hashing")
	flag.IntVar(&goroutinelimit, "g", 0, "Go Routine Limit")
	flag.BoolVar(&storePayload, "s", false, "Store Payload in addition to hashing it")
}

func main() {
	flag.Parse()
	callChan = make(chan requestRecord, callCount)
	recordedCalls = make([]requestRecord, 0, callCount)
	go storeCalls(callChan)
	// don't really care much about the seed, just avoiding using the default of 1
	randSrc := rand.NewSource(time.Now().UnixNano())
	random = rand.New(randSrc)
	server := &http.Server{Addr: ":" + strconv.Itoa(port), Handler: http.HandlerFunc(recordRequest)}
	server.MaxHeaderBytes = http.DefaultMaxHeaderBytes * headerLimit
	log.Println(server.ListenAndServe())
}

func storeCalls(c chan requestRecord) {
	swapBuf := make([]requestRecord, 0, callCount)
	for call := range c {
		// clear this buffer for filling without reallocating and add the newest call
		swapBuf = append(swapBuf, call)
		// get existing calls
		existingCalls := recordedCalls
		if len(existingCalls) >= callCount {
			// drop earliest call from list
			recordedCalls = append(swapBuf, existingCalls[1:callCount]...)
		}
		recordedCalls = append(swapBuf, existingCalls...)
		// set swapBuf to the emptied existing buffer so it can start fresh next time without reallocating
		swapBuf = existingCalls[:0]
	}
}

func setFromQueryParam(param string, val *int) error {
	if "" != param {
		num, numErr := strconv.Atoi(param)
		if nil != numErr {
			return numErr
		} else {
			*val = num
		}
	}
	return nil
}

func recordRequest(resp http.ResponseWriter, req *http.Request) {

	if goroutinelimit > 0 && runtime.NumGoroutine() > goroutinelimit {
		resp.WriteHeader(503)
		fmt.Fprintln(resp, "Hit the Go Routine limit of:", goroutinelimit)
	} else if req.URL.Path == "/favicon.ico" {
		resp.WriteHeader(404)
		fmt.Fprintln(resp, "No icon for you!")
	} else if strings.Contains(req.URL.Path, "recordedRequests") {
		for _, call := range recordedCalls {
			fmt.Fprintln(resp, call)
		}
	} else if strings.Contains(req.URL.Path, "configDelay") {
		query := req.URL.Query()
		setFromQueryParam(query.Get("delay"), &delay)
		setFromQueryParam(query.Get("variance"), &variance)
		setFromQueryParam(query.Get("chance"), &chance)
		setFromQueryParam(query.Get("limit"), &goroutinelimit)
		fmt.Fprintf(resp, "delay: %dms\nvariance: %dms\nchance: %d%%\nGo routine 'limit': %d\n", delay, variance, chance, goroutinelimit)
	} else {
		var bytesRead int64
		var rawHash, payload []byte
		var readErr error
		if bufferRequest || storePayload {
			var buf bytes.Buffer
			bytesRead, readErr = buf.ReadFrom(req.Body)
			if nil != readErr {
				resp.WriteHeader(500)
				fmt.Fprintln(resp, readErr)
				fmt.Fprintln(os.Stderr, readErr)
			}
			payload = buf.Bytes()
			rawHashArray := sha256.Sum256(payload)
			rawHash = rawHashArray[:]
		} else {
			buf := make([]byte, 0x8000)
			var justRead int
			justRead, readErr = req.Body.Read(buf)
			bytesRead += int64(justRead)
			hasher := sha256.New()
			hasher.Write(buf[:justRead])
			for justRead > 0 && readErr == nil {
				justRead, readErr = req.Body.Read(buf)
				bytesRead += int64(justRead)
				hasher.Write(buf[:justRead])
			}
			if nil != readErr && !errors.Is(readErr, io.EOF) {
				resp.WriteHeader(500)
				fmt.Fprintln(resp, readErr)
				fmt.Fprintln(os.Stderr, readErr)
			} else {
				rawHash = hasher.Sum(nil)
			}
		}

		hexHash := hex.EncodeToString(rawHash)
		callChan <- requestRecord{
			Timestamp:   time.Now(),
			Method:      req.Method,
			Uri:         req.URL.RequestURI(),
			PayloadSize: int(bytesRead),
			PayloadHash: hexHash,
			Payload:     string(payload),
		}
		fmt.Fprintln(resp, req.URL.Path, "received")

		// stall response close after writing response
		if chance > 0 {
			if chance >= random.Intn(100) {
				var shift int
				if variance > 0 {
					shift = random.Intn(variance) - variance/2
				}
				time.Sleep(time.Millisecond * time.Duration(delay+shift))
			}
		}
	}
}
