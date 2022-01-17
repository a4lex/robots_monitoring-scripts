package main

import (
	"flag"
	"fmt"
	"regexp"
	"sync"
	"time"

	h "github.com/a4lex/go-helpers"
)

type TelnetDev struct {
	sqlId    string
	hostname string
	ip       string
	login    string
	password string
}

const (
	sqlGetEponList = `SELECT id, name AS hostname, INET_NTOA(ip) AS ip, 'admin' AS login, 'gunck7iaf' AS password ` +
		`FROM epon WHERE name NOT LIKE 'fake%'AND id>0 AND country = ?`
	sqlGetEponByName = `SELECT id, name AS hostname, INET_NTOA(ip) AS ip, 'admin' AS login, 'gunck7iaf' AS password ` +
		`FROM epon WHERE name NOT LIKE 'fake%'AND id>0 AND country = ? AND name = ? LIMIT 1`

	// sqlCreateMTLink1 = "INSERT INTO mt_links (mt_iface1_id, mt_iface2_id, s1, s1_ch0, s1_ch1, ccq1, rate1, prev_byte1, s2, s2_ch0, s2_ch1, ccq2, rate2, prev_byte2, created_at, updated_at) VALUES "
	// sqlCreateMTLink2 = "('%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', NOW(), NOW()), "
	// sqlCreateMTLink3 = "ON DUPLICATE KEY UPDATE " +
	// 	"s1 = VALUE(s1), s1_ch0 = VALUE(s1_ch0), s1_ch1 = VALUE(s1_ch1), ccq1 = VALUE(ccq1), rate1 = VALUE(rate1), " +
	// 	"diff_byte1 = IF(VALUE(prev_byte1) > prev_byte1, VALUE(prev_byte1) - prev_byte1, 0), prev_byte1 = VALUE(prev_byte1), " +
	// 	"s2 = VALUE(s2), s2_ch0 = VALUE(s2_ch0), s2_ch1 = VALUE(s2_ch1), ccq2 = VALUE(ccq2), rate2 = VALUE(rate2), " +
	// 	"diff_byte2 = IF(VALUE(prev_byte2) > prev_byte2, VALUE(prev_byte2) - prev_byte2, 0), prev_byte2 = VALUE(prev_byte2), " +
	// 	"updated_at = NOW()"

	// sqlCreateMtBoard1 = "INSERT INTO mt_new_boards (name, last_ip, created_at, updated_at) VALUES "
	// sqlCreateMtBoard2 = "('%s', INET_ATON('%s'), NOW(), NOW()), "
	// sqlCreateMtBoard3 = "ON DUPLICATE KEY UPDATE  name = VALUE(name), last_ip = VALUE(last_ip), updated_at=NOW()"
)

var (
	eponName    = flag.String("epon", "", "Epon Name to fetch level")
	eponCountry = flag.String("country", "", "Epon Country to fetch level")

	connTimeout = 10 * time.Second

	chanQuery   chan string
	reActiveOnu *regexp.Regexp
)

func init() {
	//
	// Init RegExp
	//

	reActiveOnu = regexp.MustCompile(`(EPON\d+\/\d+:\d+)\s+([\da-f\.]{14})\s+([\w\-]+)\s+([\w\-]+)\s+(\d+)\s+(\d+)\s+(\d{4}\.\d{2}\.\d{2}\.\d{2}\:\d{2}\:\d{2})\s+(\d{4}\.\d{2}\.\d{2}\.\d{2}\:\d{2}\:\d{2})\s(power-off|unknow|wire-down)\s+(\d+\.\d{2}\:\d{2}\:\d{2})`)
}

func process() {

	wgQueryQueue := &sync.WaitGroup{}
	chanQuery := mysqli.InitQueryQueue(wgQueryQueue)

	//
	// Select Epon List from DB
	//

	var listDevice []map[string]string
	if *eponName != "" {
		listDevice = mysqli.DBSelectList(sqlGetEponByName, *eponCountry, *eponName)
	} else {
		listDevice = mysqli.DBSelectList(sqlGetEponList, *eponCountry)
	}

	// start N-workers
	wgEponQueue := &sync.WaitGroup{}
	eponChannel := make(chan *TelnetDev)
	for i := 0; i < *threadCount; i++ {
		wgEponQueue.Add(1)
		go grabeEponQueue(wgEponQueue, i, chanQuery, eponChannel)
	}

	// pick data from MT and store it in DB
	for _, dev := range listDevice {
		eponChannel <- &TelnetDev{dev["id"], dev["hostname"], dev["ip"], dev["login"], dev["password"]}
	}

	close(eponChannel)
	wgEponQueue.Wait()

	close(chanQuery)
	wgQueryQueue.Wait()
}

func grabeEponQueue(wg *sync.WaitGroup, num int, chanQuery chan string, eponChannel chan *TelnetDev) {
	defer wg.Done()

	funcName := fmt.Sprintf("grabeEponQueue[%d]", num)
	start := time.Now().Unix()
	l.Printf(h.FUNC, "Start: %s", funcName)

	for epon := range eponChannel {

		//
		// Connect to BDCom
		//

		l.Printf(h.INFO, fmt.Sprintf("Try connect to %s:23", epon.ip))

		t, err := MyConnect("tcp", fmt.Sprintf("%s:23", epon.ip), connTimeout,
			func(msg string) { l.Printf(h.INFO, msg) })

		if err != nil {
			l.Printf(h.ERROR, fmt.Sprintf("Can not connect to %s:23, error: %s", epon.ip, err))
			continue
		}

		t.SetUnixWriteMode(true)
		defer t.Close()

		//
		// Authorize
		//

		t.ResetCommandChainState().
			Expect("sername: ").
			SendLine(epon.login).
			Expect("assword: ").
			SendLine(epon.password).
			Expect(">").
			SendLine("enable").
			Expect("assword:", "#").
			SendLine(epon.password).
			Expect("#")

		if !t.GetCommandChainState() {
			l.Printf(h.ERROR, fmt.Sprintf("Can not authorize on %s:23", epon.ip))
			continue
		}

		l.Printf(h.INFO, fmt.Sprintf("Success auth on %s:23", epon.ip))

		//
		// Grabe epon iface
		//
		t.SendLine("show epon active-onu").ReadUntil('#')

		fmt.Println("____________________")
		fmt.Println(t.lineData)
		fmt.Println("____________________")
		if !t.GetCommandChainState() {
			l.Printf(h.ERROR, fmt.Sprintf("Can not exec command 'show epon active-onu' on %s:23", epon.ip))
			continue
		}

		activeOnu := t.SplitResponceByRegEx(reActiveOnu)
		for i := 0; i < len(activeOnu); i++ {
			fmt.Printf("%s - %s\r\n", activeOnu[i][1], activeOnu[i][2])
		}
	}

	l.Printf(h.FUNC, "Stop: %s - %d, diration: %d", funcName, time.Now().Unix(), time.Now().Unix()-start)
}

// Convert btes array to string

// gp2.mir9.tmn#sho epon optical-transceiver-diagnosis
//  interface    Temperature(degree)    Voltage(V)    Current(mA)    TxPower(dBm)
// -----------  ---------------------  ------------  -------------  --------------
// epon0/1      35.0                   3.3           35.4           4.4
// epon0/2      36.5                   3.2           10.6           7.6
// epon0/3      36.8                   3.3           12.2           5.0
// epon0/4      37.2                   3.2           11.7           7.6
//  interface    RxPower(dBm)
// -----------  --------------
// epon0/1:2    -27.2
// epon0/1:4    -28.7
// epon0/1:5    -27.0
// epon0/1:6    -25.7
// epon0/1:8    -26.3
// epon0/1:9    -25.8
// epon0/1:11   -25.9
// epon0/1:12   -27.2
// epon0/1:13   -27.2
// epon0/1:14   -26.2
