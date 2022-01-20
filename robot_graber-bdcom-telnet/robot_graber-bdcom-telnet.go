package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	h "github.com/a4lex/go-helpers"
	"github.com/gosnmp/gosnmp"
	"github.com/ziutek/rrd"
)

type EponDevice struct {
	sqlId     string
	hostname  string
	ip        string
	login     string
	password  string
	snmpVer   string
	community string
}

const (
	oidName      = ".1.3.6.1.4.1.3320.9.64.4.1.1.2"
	oidFDB       = ".1.3.6.1.4.1.3320.152.1.1.3"
	oidMAC       = ".1.3.6.1.4.1.3320.101.10.1.1.3"
	oidLevelOnu  = ".1.3.6.1.4.1.3320.101.10.5.1.5"
	oidLevelPon0 = ".1.3.6.1.4.1.3320.9.183.1.1.5"
	oidLevelPon1 = ".1.3.6.1.4.1.3320.101.108.1.3"

	sqlGetEponList = `SELECT id, name AS hostname, INET_NTOA(ip) AS ip, snmp_ro AS comunity, 'admin' AS login, 'gunck7iaf' AS password ` +
		`FROM epon WHERE name NOT LIKE 'fake%'AND id>0 AND country = ?`
	sqlGetEponByName = `SELECT id, name AS hostname, INET_NTOA(ip) AS ip, snmp_ro AS comunity, 'admin' AS login, 'gunck7iaf' AS password ` +
		`FROM epon WHERE name NOT LIKE 'fake%'AND id>0 AND country = ? AND name = ? LIMIT 1`

	sqlCallUpdateUseroOnu = `CALL update_user_onu(create_or_update_onu(?, ?, ?, ?, ?, ?, ?, ?), ?)`
	sqlUpdateInactiveOnu  = `UPDATE onu SET dereg_reason=?, change_state=IF(dereg_reason=?, 0, 1) WHERE eponid=? AND mac=? LIMIT 1`

	sqlInsertTaskForSendMail = `CALL ADD_TASK('epon check_pon_line all', 'radius', 'self')`
	sqlCheckBrokenLine       = `` +
		`SELECT IF(COUNT(*) > 0, 'TRUE', 'FALSE') AS send_mail FROM (` +
		`SELECT COUNT(*) AS total_if,  SUM(IF(change_state=1, 1, 0)) AS change_state_if FROM onu GROUP BY eponid, SUBSTRING(name, 1,7)` +
		`) AS t WHERE (change_state_if*100)/total_if>? AND change_state_if>?`
)

var (
	telnetTimeout = flag.Int("telnet-timeout", 30, "Telnet Timeout for waiting responce from device")
	telnetRetries = flag.Int("telnet-retries", 3, "Telnet Retries connect to device")

	eponName         = flag.String("epon", "", "Epon Name to fetch level")
	eponCountry      = flag.String("country", "", "Epon Country to fetch level")
	ifChStatePercent = flag.String("ifchst-per", "25", "Epon Country to fetch level")
	ifChStateCount   = flag.String("ifchst-count", "5", "Epon Country to fetch level")

	chanQuery chan h.Query

	reIfEponName, reActiveOnu, reInactiveOnu, reClientFDB, reFormatMAC *regexp.Regexp
)

func init() {
	//
	// Init RegExp
	//

	reIfEponName = regexp.MustCompile(`(?i)^(EPON\d+\/\d+:\d+)$`)
	reActiveOnu = regexp.MustCompile(`(?i)(EPON\d+\/\d+:\d+)\s+([a-f\d\.]{14})\s+([\w\-]+)\s+([\w\-]+)\s+(\d+)\s+(\d+)\s+(\d{4}[\.-]\d{2}[\.-]\d{2}[\.\s]\d{2}\:\d{2}\:\d{2})\s+(\d{4}[\.-]\d{2}[\.-]\d{2}[\.\s]\d{2}\:\d{2}\:\d{2})\s+([\w\-\_]+)\s+(\d+\.\d{2}\:\d{2}\:\d{2})`)
	reInactiveOnu = regexp.MustCompile(`(?i)(EPON\d+\/\d+:\d+)\s+([a-f\d\.]{14})\s+([\w]+)\s+(\d{4}[\.-]\d{2}[\.-]\d{2}[\.\s]\d{2}\:\d{2}\:\d{2})\s+(\d{4}[\.-]\d{2}[\.-]\d{2}[\.\s]\d{2}\:\d{2}\:\d{2})\s+([\w\-\_]+)\s+(\d+\.\d{2}\:\d{2}\:\d{2})`)
	// reRXlLevel = regexp.MustCompile(`(?i)(EPON\d+\/\d+:\d+)\s+(\-\d+\.\d+)`)
	reClientFDB = regexp.MustCompile(`(?i)([a-f0-9]{4}\.[a-f0-9]{4}\.[a-f0-9]{4})`)
	reFormatMAC = regexp.MustCompile(`(?i)([a-f0-9]{2})([a-f0-9]{2})\.([a-f0-9]{2})([a-f0-9]{2})\.([a-f0-9]{2})([a-f0-9]{2})`)
}

func process() {

	timeUpdRRD = time.Now()
	l.Printf(h.DEBUG, "Time for RRD DB update fixed to: %s", timeUpdRRD.Format("2006-01-02 15:04:05"))

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
	eponChannel := make(chan *EponDevice)
	for i := 0; i < *threadCount; i++ {
		wgEponQueue.Add(1)
		go grabeEponQueue(wgEponQueue, i, chanQuery, eponChannel)
	}

	// pick data from EPON and store it in DB
	for _, dev := range listDevice {
		eponChannel <- &EponDevice{dev["id"], dev["hostname"], dev["ip"], dev["login"], dev["password"], "v2c", dev["comunity"]}
	}

	close(eponChannel)
	wgEponQueue.Wait()

	if send_mail, ok := mysqli.DBSelectRow(sqlCheckBrokenLine, *ifChStatePercent, *ifChStateCount)["send_mail"]; ok && send_mail == "TRUE" {
		mysqli.DBQuery(sqlInsertTaskForSendMail)
	}

	close(chanQuery)
	wgQueryQueue.Wait()
}

func grabeEponQueue(wg *sync.WaitGroup, num int, chanQuery chan h.Query, eponChannel chan *EponDevice) {
	defer wg.Done()

	funcName := fmt.Sprintf("grabeEponQueue[%d]", num)
	start := time.Now().Unix()
	l.Printf(h.FUNC, "Start: %s", funcName)

	snmpQuery := make([]string, 0)

	var err error
	var nextID string
	var rowResult [5]string
	var snmpResult *gosnmp.SnmpPacket
	var eponIfList map[string]map[string]string

	var telnetConnectAttempt int

	for epon := range eponChannel {

		eponIfList = make(map[string]map[string]string)

		// THIS IS LEGACY
		rrdDbPath := fmt.Sprintf("%s/%s", *dirRRD, epon.sqlId)
		if _, err := os.Stat(rrdDbPath); os.IsNotExist(err) {
			os.MkdirAll(rrdDbPath, os.ModePerm)
			l.Printf(h.INFO, "Create dir for RRD files: %s", rrdDbPath)
		}

		//
		// Fetch iface info via SNMP
		//

		snmpQuery = []string{oidName, oidMAC, oidLevelOnu, oidLevelPon0, oidLevelPon1}
		snmpInst := GetSnmpCon(epon.ip, epon.snmpVer, epon.community)
		if err = snmpInst.Connect(); err != nil {
			l.Printf(h.INFO, "%s: Host %s got connect error: %v", funcName, epon.ip, err)
			continue
		}
		defer snmpInst.Conn.Close()

		for {
			if snmpResult, err = snmpInst.GetNext(snmpQuery); err != nil || !strings.HasPrefix(snmpResult.Variables[0].Name, oidName) {
				break
			}

			for id, snmpPDU := range snmpResult.Variables {
				if id == 0 {
					snmpQuery[0] = snmpPDU.Name
					nextID = strings.Replace(snmpQuery[0], oidName, "", -1)
					snmpQuery[1] = fmt.Sprintf("%s%s", oidMAC, nextID)
					snmpQuery[2] = fmt.Sprintf("%s%s", oidLevelOnu, nextID)
					snmpQuery[3] = fmt.Sprintf("%s%s", oidLevelPon0, nextID)
					snmpQuery[4] = fmt.Sprintf("%s%s", oidLevelPon1, nextID)
				}

				switch snmpPDU.Type {
				case gosnmp.OctetString:
					rowResult[id] = string(snmpPDU.Value.([]byte))
				default:
					rowResult[id] = gosnmp.ToBigInt(snmpPDU.Value).String()
				}
			}

			if reIfEponName.Match([]byte(rowResult[0])) && rowResult[3] != "-65535" && rowResult[4] != "-65535" {
				ifName := strings.ToUpper(rowResult[0])
				eponIfList[ifName] = make(map[string]string)
				eponIfList[ifName]["mac"] = strings.ToUpper(fmt.Sprintf("%v", net.HardwareAddr(rowResult[1])))
				eponIfList[ifName]["tx"] = rowResult[2]
				if rowResult[3] == "1" {
					eponIfList[ifName]["rx"] = rowResult[4]
				} else {
					eponIfList[ifName]["rx"] = rowResult[3]
				}

				//
				// THIS IS LEGACY
				// we don't need to update RDD-DB here
				// For this we have a special robot_updater-rrd
				// But now not enough time for it implementation and installation
				//
				tx := fmt.Sprintf("%s.%s", eponIfList[ifName]["tx"][0:len(eponIfList[ifName]["tx"])-1], eponIfList[ifName]["tx"][len(eponIfList[ifName]["tx"])-1:])
				rx := fmt.Sprintf("%s.%s", eponIfList[ifName]["rx"][0:len(eponIfList[ifName]["rx"])-1], eponIfList[ifName]["rx"][len(eponIfList[ifName]["rx"])-1:])
				rrdDbPath := fmt.Sprintf("%s/%s/%s", *dirRRD, epon.sqlId, strings.ToLower(strings.ReplaceAll(eponIfList[ifName]["mac"], ":", "")))
				if err := LEGACY_RRDUpdate(rrdDbPath, timeUpdRRD, tx, rx); err != nil {
					if _, err := os.Stat(rrdDbPath); os.IsNotExist(err) {
						if _, err := LEGACY_RRDCreate(rrdDbPath, "GAUGE", -50, 0, 300); err != nil {
							l.Printf(h.ERROR, "Can not create rrddb: %s - %s", rrdDbPath, err)
						}
					}
				}
			}
		}

		//
		// Connect to BDCom
		//

		telnetConnectAttempt = *telnetRetries

		l.Printf(h.INFO, fmt.Sprintf("Try connect to %s:23", epon.ip))

		t, err := h.TelnetConnect("tcp", fmt.Sprintf("%s:23", epon.ip), time.Duration(*telnetTimeout)*(time.Second),
			func(msg string) { l.Printf(h.INFO, msg) })

		if err != nil {
			l.Printf(h.ERROR, fmt.Sprintf("Can not connect to %s:23, error: %s", epon.ip, err))
			continue
		}

		t.SetUnixWriteMode(true)
		defer t.Close()

		//
		// Authorize via telnet
		//

		authorize(t, epon.login, epon.password)

		if !t.IsConnected() {
			l.Printf(h.ERROR, fmt.Sprintf("Can not authorize on %s:23", epon.ip))
			continue
		}

		l.Printf(h.INFO, fmt.Sprintf("Success auth on %s:23", epon.ip))

		//
		// Grabe active epon iface
		//

		if !t.SendLine("show epon active-onu").ReadUntil('#').IsConnected() {
			l.Printf(h.ERROR, fmt.Sprintf("Can not exec command 'show epon active-onu' on %s:23", epon.ip))
			continue
		}

		for _, onu := range t.FindAllStringSubmatch(reActiveOnu) {
			ifName := strings.ToUpper(onu[1])
			if _, ok := eponIfList[ifName]; ok {
				eponIfList[ifName]["distance"] = onu[5]
				eponIfList[ifName]["rrt"] = onu[6]
				eponIfList[ifName]["dereg_reason"] = onu[9]

				if !t.IsConnected() && telnetConnectAttempt > 0 {
					telnetConnectAttempt--
					t.Reconnect()
					authorize(t, epon.login, epon.password)
					l.Printf(h.INFO, fmt.Sprintf("Try reconnect to %s:23, attempt for reconnect: %d", epon.ip, telnetConnectAttempt))
				}
				t.SendLine("show mac address-table dynamic interface %s", ifName).ReadUntil('#')
				if !t.IsConnected() {
					l.Printf(h.ERROR, fmt.Sprintf("Can not exec command 'show mac address-table dynamic interface %s' on %s:23", ifName, epon.ip))
					continue
				}

				var _macs string
				for _, mac := range t.FindAllString(reClientFDB) {
					_mac := strings.ToUpper(reFormatMAC.ReplaceAllString(mac, "$1:$2:$3:$4:$5:$6"))
					if _mac == eponIfList[ifName]["mac"] {
						continue
					}
					_macs += _mac
				}

				if len(_macs) > 0 {
					if len(_macs) > 255 {
						l.Printf(h.ERROR, fmt.Sprintf("To much mac in iface: %s epon: %s. MYSQL proccedure update_user_onu(INT(16), VARCHAR(255)) can drop it", ifName, epon.ip))
					}

					chanQuery <- h.Query{Query: sqlCallUpdateUseroOnu, Args: []interface{}{
						epon.sqlId,
						ifName,
						eponIfList[ifName]["mac"],
						eponIfList[ifName]["tx"],
						eponIfList[ifName]["rx"],
						eponIfList[ifName]["distance"],
						eponIfList[ifName]["rrt"],
						eponIfList[ifName]["dereg_reason"],
						_macs,
					}}

				}
			}
		}

		//
		// Grabe inactive epon iface
		//

		if !t.SendLine("show epon inactive-onu").ReadUntil('#').IsConnected() {
			l.Printf(h.ERROR, fmt.Sprintf("Can not exec command 'show epon inactive-onu' on %s:23", epon.ip))
			continue
		}

		for _, onu := range t.FindAllStringSubmatch(reInactiveOnu) {
			chanQuery <- h.Query{Query: sqlUpdateInactiveOnu, Args: []interface{}{
				onu[6], //dereg_reason
				onu[6], //dereg_reason
				epon.sqlId,
				strings.ToUpper(reFormatMAC.ReplaceAllString(onu[2], "$1:$2:$3:$4:$5:$6")), //mac
			}}
		}

		t.
			SendLine("exit").
			Expect(">").
			SendLine("exit")
	}

	l.Printf(h.FUNC, "Stop: %s - %d, diration: %d", funcName, time.Now().Unix(), time.Now().Unix()-start)
}

func authorize(t *h.MyTelnet, login, password string) {
	t.
		Expect("sername: ").
		SendLine(login).
		Expect("assword: ").
		SendLine(password).
		Expect(">").
		SendLine("enable").
		Expect("assword:", "#").
		SendLine(password).
		Expect("#")
}

//
// RRDCreate - create file of RRD DB
//
func LEGACY_RRDCreate(dbfile, counterType string, min, max int, step uint) (*rrd.Creator, error) {

	c := rrd.NewCreator(dbfile, time.Now(), step)
	c.DS("onu", counterType, step, min, max)
	c.DS("pon", counterType, step, min, max)
	c.RRA("AVERAGE", 0.5, 1, 288)
	c.RRA("LAST", 0.5, 1, 288)
	c.RRA("MIN", 0.5, 1, 288)
	c.RRA("MAX", 0.5, 1, 288)
	c.RRA("AVERAGE", 0.5, 7, 288)
	c.RRA("LAST", 0.5, 7, 288)
	c.RRA("MIN", 0.5, 7, 288)
	c.RRA("MAX", 0.5, 7, 288)
	c.RRA("AVERAGE", 0.5, 30, 288)
	c.RRA("LAST", 0.5, 30, 288)
	c.RRA("MIN", 0.5, 30, 288)
	c.RRA("MAX", 0.5, 30, 288)
	c.RRA("AVERAGE", 0.5, 365, 288)
	c.RRA("LAST", 0.5, 365, 288)
	c.RRA("MIN", 0.5, 365, 288)
	c.RRA("MAX", 0.5, 365, 288)

	if err := c.Create(true); err != nil {
		l.Printf(h.ERROR, "Can not create RRD DB: %s, counterType: %s, min: %d, max: %d", dbfile, counterType, min, max)
		return nil, err
	} else {
		l.Printf(h.DEBUG, "Create RRD DB: %s, counterType: %s, min: %d, max: %d", dbfile, counterType, min, max)
		return c, nil
	}
}

//
// RRDUpdate - update RRD DB file with given value and time
//
func LEGACY_RRDUpdate(dbfile string, time time.Time, val1, val2 string) error {
	u := rrd.NewUpdater(dbfile)
	if err := u.Update(time, val1, val2); err != nil {
		l.Printf(h.DEBUG, "Update is failed RRD DB: %s, time: %s, val1: %s, val2: %s, error: %s", dbfile, time.Format("2006-01-02 15:04:05"), val1, val2, err)
		return err
	}
	l.Printf(h.DEBUG, "Update RRD DB: %s, time: %s, val1: %s, val2: %s", dbfile, time.Format("2006-01-02 15:04:05"), val1, val2)
	return nil
}
