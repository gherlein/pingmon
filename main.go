package main

import (
	"fmt"
	"github.com/coreos/go-systemd/daemon"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/viper"
	"github.com/tatsushid/go-fastping"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type response struct {
	addr *net.IPAddr
	rtt  time.Duration
}

var (
	rtt_set []time.Duration
	rtt_tot time.Duration
	rtt_ms  float64
	i       int32
	rtt     = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "ping_google",
		Subsystem: "network",
		Name:      "rtt",
		Help:      "network round trip time for pings to google.com",
	})
	host        string = ""
	port        string = ""
	count       int32  = 15
	logfile     string = "/var/log/pingmon.log"
	unreachable bool   = false
)

func init() {

	if host == "" || port == "" {
		viper.SetConfigName("pingmon") // no need to include file extension
		viper.AddConfigPath(".")
		viper.AddConfigPath("/etc/")

		err := viper.ReadInConfig()
		if err != nil {

		} else {
			host = viper.GetString("target.host")
			port = viper.GetString("exporter.port")
			count = viper.GetInt32("target.count")
		}
	}
	prometheus.MustRegister(rtt)
}

func main() {

	// set the log file
	f, err := os.OpenFile(logfile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal(err)
	}
	log.SetOutput(f)
	defer func() {
		log.Println("exiting")
		f.Sync()
		f.Close()
	}()
	log.Printf("STARTED, %s\n", host)

	p := fastping.NewPinger()
	netProto := "ip4:icmp"
	if strings.Index(host, ":") != -1 {
		netProto = "ip6:ipv6-icmp"
	}
	ra, err := net.ResolveIPAddr(netProto, host)
	if err != nil {
		fmt.Println(err)
		log.Fatal("UNRESOLVEABLE TARGET\n")
		os.Exit(1)
	}

	results := make(map[string]*response)
	results[ra.String()] = nil
	p.AddIPAddr(ra)

	onRecv, onIdle := make(chan *response), make(chan bool)
	p.OnRecv = func(addr *net.IPAddr, t time.Duration) {
		onRecv <- &response{addr: addr, rtt: t}
	}
	p.OnIdle = func() {
		onIdle <- true
	}

	p.MaxRTT = time.Second
	p.RunLoop()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	signal.Notify(c, syscall.SIGTERM)

	go func() {
		http.Handle("/metrics", promhttp.Handler())
		url := fmt.Sprintf(":%s", port)
		log.Fatal(http.ListenAndServe(url, nil))
	}()

	daemon.SdNotify(false, "READY=1")
	ticker2 := time.NewTicker(time.Second * 15)
	go func() {
		for t := range ticker2.C {
			_ = t
			url := fmt.Sprintf("http://localhost:%s/metrics", port)
			_, err := http.Get(url)
			if err != nil {
				log.Println("internal health check failed")
			} else {
				daemon.SdNotify(false, "WATCHDOG=1")
			}
		}
	}()

loop:
	for {
		select {
		case <-c:
			break loop
		case res := <-onRecv:
			if _, ok := results[res.addr.String()]; ok {
				results[res.addr.String()] = res
			}
		case <-onIdle:
			i++
			for host, r := range results {
				if r == nil {
					rtt.Add(float64(0))
					unreachable = true
				} else {
					unreachable = false
					rtt_set = append(rtt_set, r.rtt)
				}
				results[host] = nil
			}
		case <-p.Done():
			if err = p.Err(); err != nil {
			}
			log.Printf("ERROR\n")
			break loop
		}
		if i == count {
			i = 0
			rtt_tot = 0
			if unreachable == true {
				log.Printf("UNREACHABLE\n")
			} else {

				for _, element := range rtt_set {
					rtt_tot += element
				}
				rtt_ms = float64(rtt_tot) / (float64(count) * float64(time.Millisecond))
				log.Printf("%f\n", rtt_ms)
				rtt.Add(float64(rtt_ms))
				rtt_set = nil
			}
		}
	}
	signal.Stop(c)
	p.Stop()
}
