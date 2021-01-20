package main

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	h "github.com/a4lex/go-helpers"
	"gopkg.in/routeros.v2"
)

const (
	sqlGetMtPassword = `SELECT GROUP_CONCAT(password SEPARATOR ';') AS passwords FROM ` +
		`(SELECT password, COUNT(*) AS size FROM devices WHERE device_type_id = 3 GROUP BY password ORDER BY size DESC) AS t`
	sqlGetMtList = `SELECT INET_NTOA(d.ip) AS ip, d.username, d.password, i.id AS iface_id, i.name AS if_name, ` +
		`i.radio_name, i.mode FROM devices d, mt_ifaces i WHERE d.id = i.device_id` // AND b.id IN (1159, 1171)`

	sqlCreateMTLink1 = "INSERT INTO mt_links (mt_iface1_id, mt_iface2_id, s1, s1_ch0, s1_ch1, ccq1, rate1, prev_byte1, s2, s2_ch0, s2_ch1, ccq2, rate2, prev_byte2, created_at, updated_at) VALUES "
	sqlCreateMTLink2 = "('%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', '%s', NOW(), NOW()), "
	sqlCreateMTLink3 = "ON DUPLICATE KEY UPDATE " +
		"s1 = VALUE(s1), s1_ch0 = VALUE(s1_ch0), s1_ch1 = VALUE(s1_ch1), ccq1 = VALUE(ccq1), rate1 = VALUE(rate1), " +
		"diff_byte1 = IF(VALUE(prev_byte1) > prev_byte1, VALUE(prev_byte1) - prev_byte1, 0), prev_byte1 = VALUE(prev_byte1), " +
		"s2 = VALUE(s2), s2_ch0 = VALUE(s2_ch0), s2_ch1 = VALUE(s2_ch1), ccq2 = VALUE(ccq2), rate2 = VALUE(rate2), " +
		"diff_byte2 = IF(VALUE(prev_byte2) > prev_byte2, VALUE(prev_byte2) - prev_byte2, 0), prev_byte2 = VALUE(prev_byte2), " +
		"updated_at = NOW()"

	sqlCreateMtBoard1 = "INSERT INTO mt_new_boards (name, last_ip, created_at, updated_at) VALUES "
	sqlCreateMtBoard2 = "('%s', INET_ATON('%s'), NOW(), NOW()), "
	sqlCreateMtBoard3 = "ON DUPLICATE KEY UPDATE  name = VALUE(name), last_ip = VALUE(last_ip), updated_at=NOW()"
)

var (
	mtPassList  []string
	mtIfaceList map[string]map[string]string

	initMtChannel chan string
	chanQuery     chan string

	reRate, reTxByte, reRxByte, signalStrength *regexp.Regexp
)

func init() {
	mtIfaceList = make(map[string]map[string]string)

	//
	// Init RegExp
	//

	reRate, _ = regexp.Compile(`^(\d+)`)
	reTxByte, _ = regexp.Compile(`^(\d+)`)
	reRxByte, _ = regexp.Compile(`(\d+)$`)
	signalStrength, _ = regexp.Compile(`^(-\d+)`)
}

func process() {

	wgQueryQueue := &sync.WaitGroup{}
	chanQuery := mysqli.InitQueryQueue(wgQueryQueue)

	//
	// Select passwords and wireless ifaces from DB
	//

	row := mysqli.DBSelectRow(sqlGetMtPassword)
	if _, ok := row["passwords"]; !ok {
		l.Printf(h.ERROR, "Can not find MT password list")
	}

	// pick all wlan ifaces
	list := mysqli.DBSelectList(sqlGetMtList)
	for _, iface := range list {
		mtIfaceList[iface["radio_name"]] = iface
	}

	// start N-workers
	wgMTQueue := &sync.WaitGroup{}
	mtChannel := make(chan string)
	for i := 0; i < *threadCount; i++ {
		wgMTQueue.Add(1)
		go grabeMTQueue(wgMTQueue, i, chanQuery, mtChannel)
	}

	// pick data from MT and store it in DB
	for _, iface := range mtIfaceList {
		if iface["mode"] == "ap-bridge" || iface["mode"] == "bridge" {
			mtChannel <- iface["radio_name"]
		}
	}

	close(mtChannel)
	wgMTQueue.Wait()

	close(chanQuery)
	wgQueryQueue.Wait()
}

func grabeMTQueue(wg *sync.WaitGroup, num int, chanQuery chan string, mtChannel chan string) {
	defer wg.Done()

	funcName := fmt.Sprintf("grabeMTQueue[%d]", num)
	start := time.Now().Unix()
	l.Printf(h.FUNC, "Start: %s", funcName)

	var c *routeros.Client
	var err error
	var newMtLinks, newMtBoards string

	for mtRadioName := range mtChannel {
		mtBase := mtIfaceList[mtRadioName]
		newMtLinks, newMtBoards = "", ""

		if c, err = dial(fmt.Sprintf("%s:8728", mtBase["ip"]), mtBase["username"], mtBase["password"], 3*time.Second); err != nil {
			l.Printf(h.INFO, err.Error())
			continue
		}
		defer c.Close()

		reply, err := c.Run("/interface/wireless/registration-table/print", fmt.Sprintf("?interface=%s", mtBase["if_name"]))
		if err != nil {
			l.Printf(h.ERROR, err.Error())
		}

	BAD_RESPONCE:
		for _, re := range reply.Re {
			if mtClient, ok := mtIfaceList[re.Map["radio-name"]]; ok {

				for _, v := range []string{
					"tx-rate", "rx-rate", "bytes", "bytes",
					"tx-signal-strength", "tx-signal-strength-ch0", "tx-signal-strength-ch1",
					"tx-ccq", "signal-strength", "signal-strength-ch0", "signal-strength-ch1", "rx-ccq"} {
					if _, ok := re.Map[v]; !ok {
						l.Printf(h.INFO, fmt.Sprintf("Bad responce for %s in value: %s", re.Map["radio-name"], v))
						continue BAD_RESPONCE
					}
				}

				tx := reTxByte.FindString(re.Map["bytes"])
				rx := reRxByte.FindString(re.Map["bytes"])
				txRate := reRate.FindString(re.Map["tx-rate"])
				rxRate := reRate.FindString(re.Map["rx-rate"])
				txSignStr := signalStrength.FindString(re.Map["tx-signal-strength"])
				txSignStrCh0 := signalStrength.FindString(re.Map["tx-signal-strength-ch0"])
				txSignStrCh1 := signalStrength.FindString(re.Map["tx-signal-strength-ch1"])
				rxSignStr := signalStrength.FindString(re.Map["signal-strength"])
				rxSignStrCh0 := signalStrength.FindString(re.Map["signal-strength-ch0"])
				rxSignStrCh1 := signalStrength.FindString(re.Map["signal-strength-ch1"])

				newMtLinks += fmt.Sprintf(sqlCreateMTLink2, mtBase["iface_id"], mtClient["iface_id"],
					txSignStr, txSignStrCh0, txSignStrCh1, re.Map["tx-ccq"], txRate, tx,
					rxSignStr, rxSignStrCh0, rxSignStrCh1, re.Map["rx-ccq"], rxRate, rx)

			} else {
				for _, v := range []string{"radio-name", "last-ip"} {
					if _, ok := re.Map[v]; !ok {
						continue BAD_RESPONCE
					}
				}
				newMtBoards += fmt.Sprintf(sqlCreateMtBoard2, re.Map["radio-name"], re.Map["last-ip"])
			}
		}

		if newMtLinks != "" {
			chanQuery <- (sqlCreateMTLink1 + strings.TrimRight(newMtLinks, ", ") + sqlCreateMTLink3)
		}

		// if newMtBoards != "" {
		// 	chanQuery <- (sqlCreateMtBoard1 + strings.TrimRight(newMtBoards, ", ") + sqlCreateMtBoard3)
		// }

	}

	l.Printf(h.FUNC, "Stop: %s - %d, diration: %d", funcName, time.Now().Unix(), time.Now().Unix()-start)
}

//
// Dial connects and logs in to a RouterOS device.
//
func dial(address, username, password string, timeout time.Duration) (*routeros.Client, error) {
	d := net.Dialer{Timeout: timeout}
	if conn, err := d.Dial("tcp", address); err != nil {
		return nil, err
	} else if c, err := routeros.NewClient(conn); err != nil {
		conn.Close()
		return nil, err
	} else if err = c.Login(username, password); err != nil {
		c.Close()
		return nil, err
	} else {
		return c, nil
	}
}
