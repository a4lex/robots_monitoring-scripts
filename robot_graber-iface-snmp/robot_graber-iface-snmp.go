package main

import (
	"flag"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	h "github.com/a4lex/go-helpers"
	"github.com/gosnmp/gosnmp"
)

const (
	oidIfDesc  = ".1.3.6.1.2.1.2.2.1.2."
	oidIfType  = ".1.3.6.1.2.1.2.2.1.3."
	oidIfSpeed = ".1.3.6.1.2.1.2.2.1.5."

	sqlGetDeviceAll = `SELECT d.id, d.device_type_id, INET_NTOA(d.ip) AS ip, t.snmp_version, d.community ` +
		`FROM devices d, device_types t WHERE t.id = d.device_type_id AND d.monitor = 1`
	sqlGetDeviceByID   = sqlGetDeviceAll + ` AND d.id = ? LIMIT 1`
	sqlGetDeviceByType = sqlGetDeviceAll + ` AND d.device_type_id IN `

	sqlGetIface         = `SELECT id, oid FROM device_ifaces WHERE device_id = ? AND iface_type_id = ?`
	sqlGetSnmpTemplates = `SELECT di.device_type_id AS device_type_id, di.iface_type_id AS iface_type_id, ` +
		`CONCAT(t.shared, '/', t.name) AS path, t.query, t.rate, t.type AS couter_type, t.min, t.max, t.step, t.threshold ` +
		`FROM device_type_iface_types di, iface_type_snmp_template p, snmp_templates t ` +
		`WHERE di.id=p.dev_iface_type_id AND t.id=snmp_template_id`

	sqlCreateDevicePort1 = `INSERT INTO device_ifaces (device_id, oid, name, speed, iface_type_id, created_at, updated_at) VALUES `
	sqlCreateDevicePort2 = `('%s', '%s', '%s', '%s', '%s', NOW(), NOW()),`
	sqlCreateDevicePort3 = ` ON DUPLICATE KEY UPDATE iface_type_id=VALUE(iface_type_id), speed=VALUE(speed), name=VALUE(name), updated_at=NOW()`
)

var (
	deviceID     = flag.Int("device", 0, "Device ID to update interface list")
	isFetchIface = flag.Bool("fetch-iface", false, "Fetch all interface from device and store it in DB")
	isFetchData  = flag.Bool("fetch-data", false, "Fetch data from interface and store it in DB")

	snmpTemplates map[string]map[string][]*OID
)

func process() {
	wgQueryQueue := &sync.WaitGroup{}
	chanQuery := mysqli.InitQueryQueue(wgQueryQueue)

	if isFlagPassed("fetch-iface") {
		l.Printf(h.INFO, "Fetch all interface from device and store it in DB")
		fetchIface(chanQuery)
	}

	if isFlagPassed("fetch-data") {
		l.Printf(h.INFO, "Fetch data from interface and store it in DB")
		fetchInfo(chanQuery)
	}

	close(chanQuery)
	wgQueryQueue.Wait()
}

func fetchIface(chanQuery chan string) {
	funcName := "fetchIface"
	start := time.Now().Unix()
	l.Printf(h.FUNC, "Start: %s", funcName)

	//
	// Start N-workers for process all devices
	//

	wgDeviceQueue := &sync.WaitGroup{}
	deviceChannel := make(chan *Device)
	for i := 0; i < *threadCount; i++ {
		wgDeviceQueue.Add(1)
		go queueGrabeIfaces(wgDeviceQueue, i, chanQuery, deviceChannel)
	}

	//
	// Loop throw picked device
	//

	var listDevice []map[string]string
	if *deviceID != 0 {
		listDevice = mysqli.DBSelectList(sqlGetDeviceByID, *deviceID)
	} else {
		listDevice = mysqli.DBSelectList(sqlGetDeviceAll)
	}

	for _, dev := range listDevice {
		deviceChannel <- &Device{dev["id"], dev["device_type_id"], dev["ip"], dev["snmp_version"], dev["community"]}
	}

	close(deviceChannel)
	wgDeviceQueue.Wait()

	l.Printf(h.FUNC, "Stop: %s - %d, diration: %d", funcName, time.Now().Unix(), time.Now().Unix()-start)
}

func queueGrabeIfaces(wg *sync.WaitGroup, num int, chanStoreIface chan string, deviceChannel chan *Device) {
	defer wg.Done()
	funcName := fmt.Sprintf("grabeIfaces[%d]", num)
	start := time.Now().Unix()
	l.Printf(h.FUNC, "Start: %s", funcName)

	portInfo := make([]interface{}, 0, 16)
	snmpQuery := make([]string, 0)
	for dev := range deviceChannel {

		var err error
		var snmpResult *gosnmp.SnmpPacket
		snmpQuery = []string{
			strings.TrimRight(oidIfDesc, "."),
			strings.TrimRight(oidIfSpeed, "."),
			strings.TrimRight(oidIfType, "."),
		}

		snmpInst := GetSnmpCon(dev.ip, dev.snmpVer, dev.community)
		if err := snmpInst.Connect(); err != nil {
			l.Printf(h.INFO, "%s: Host %s got connect error: %v", funcName, dev.ip, err)
			continue
		}
		defer snmpInst.Conn.Close()

		for {
			if snmpResult, err = snmpInst.GetNext(snmpQuery); err != nil || len(snmpResult.Variables) != 3 || !strings.HasPrefix(snmpResult.Variables[0].Name, oidIfDesc) {
				break
			}
			portInfo = append(portInfo, dev.id)
			portInfo = append(portInfo, strings.TrimPrefix(snmpResult.Variables[0].Name, oidIfDesc))
			for id, snmpPDU := range snmpResult.Variables {
				snmpQuery[id] = snmpPDU.Name
				switch snmpPDU.Type {
				case gosnmp.OctetString:
					portInfo = append(portInfo, string(snmpPDU.Value.([]byte)))
					l.Printf(h.DEBUG, "Found new iface: %s", string(snmpPDU.Value.([]byte)))
				default:
					portInfo = append(portInfo, gosnmp.ToBigInt(snmpPDU.Value).String())
				}
			}
		}

		if len(portInfo) == 0 {
			l.Printf(h.INFO, "%s: Host %s got emprty responce", funcName, dev.ip)
			continue
		}

		chanStoreIface <- sqlCreateDevicePort1 +
			strings.TrimRight(fmt.Sprintf(strings.Repeat(sqlCreateDevicePort2, len(portInfo)/5), portInfo...), ",") +
			sqlCreateDevicePort3

		portInfo = portInfo[:0]
	}

	l.Printf(h.FUNC, "Stop: %s - %d, diration: %d", funcName, time.Now().Unix(), time.Now().Unix()-start)
}

func fetchInfo(chanQuery chan string) {
	funcName := "fetchInfo"
	start := time.Now().Unix()
	l.Printf(h.FUNC, "Start: %s", funcName)

	timeUpdRRD = time.Now()
	l.Printf(h.DEBUG, "Time for RRD DB update fixed to: %s", timeUpdRRD.Format("2006-01-02 15:04:05"))

	//
	// Select SMMP templates and prepare them for use
	//

	deviceTypes := make([]interface{}, 0)
	snmpTemplates = make(map[string]map[string][]*OID)
	for _, row := range mysqli.DBSelectList(sqlGetSnmpTemplates) {

		if _, ok := snmpTemplates[row["device_type_id"]]; !ok {
			snmpTemplates[row["device_type_id"]] = make(map[string][]*OID)
			deviceTypes = append(deviceTypes, row["device_type_id"])
		}

		if _, ok := snmpTemplates[row["device_type_id"]][row["iface_type_id"]]; !ok {
			snmpTemplates[row["device_type_id"]][row["iface_type_id"]] = []*OID{
				CreateSnmpTemplate(row),
			}
		} else {
			snmpTemplates[row["device_type_id"]][row["iface_type_id"]] = append(
				snmpTemplates[row["device_type_id"]][row["iface_type_id"]],
				CreateSnmpTemplate(row),
			)
		}
	}

	//
	// Start N-workers for process all devices
	//

	wgDeviceQueue := &sync.WaitGroup{}
	deviceChannel := make(chan *Device)
	for i := 0; i < *threadCount; i++ {
		wgDeviceQueue.Add(1)
		go queueGrabeIfacesData(wgDeviceQueue, i, chanQuery, deviceChannel)
	}

	//
	// Loop throw picked device
	//

	var listDevice []map[string]string
	if *deviceID != 0 {
		listDevice = mysqli.DBSelectList(sqlGetDeviceByID, *deviceID)
	} else {
		listDevice = mysqli.DBSelectList(sqlGetDeviceByType +
			fmt.Sprintf(
				"("+strings.TrimRight(strings.Repeat("%s, ", len(deviceTypes)), ", ")+")",
				deviceTypes...,
			))
	}

	for _, dev := range listDevice {
		deviceChannel <- &Device{dev["id"], dev["device_type_id"], dev["ip"], dev["snmp_version"], dev["community"]}
	}

	close(deviceChannel)
	wgDeviceQueue.Wait()

	l.Printf(h.FUNC, "Stop: %s - %d, diration: %d", funcName, time.Now().Unix(), time.Now().Unix()-start)
}

func queueGrabeIfacesData(wg *sync.WaitGroup, num int, chanStoreIface chan string, deviceChannel chan *Device) {
	defer wg.Done()
	funcName := fmt.Sprintf("queueGrabeIfacesData[%d]", num)
	start := time.Now().Unix()
	l.Printf(h.FUNC, "Start: %s", funcName)

	from, to := 0, 0
	snmpResp := make([]*big.Float, 0)
	snmpVars := make([]Iface2SNMP, 0)
	snmpQueries := make([]string, 0)

LOOP_PROCESS_DEVICE:
	for dev := range deviceChannel {

		from, to = 0, 0
		snmpQueries = snmpQueries[:0]
		snmpVars = snmpVars[:0]
		snmpResp = snmpResp[:0]

		//
		// connect to device via snmp
		//
		snmpInst := GetSnmpCon(dev.ip, dev.snmpVer, dev.community)
		if err := snmpInst.Connect(); err != nil {
			l.Printf(h.INFO, "%s: Host %s got connect error: %v", funcName, dev.ip, err)
			continue LOOP_PROCESS_DEVICE
		}
		defer snmpInst.Conn.Close()

		//
		// build SNMP Templates list fot fetch from device
		//
		for ifType, snmpTemplatePerDevice := range snmpTemplates[dev.devType] {
			for _, iface := range mysqli.DBSelectList(sqlGetIface, dev.id, ifType) {
				for _, template := range snmpTemplatePerDevice {
					snmpQueries = append(snmpQueries, fmt.Sprintf("%s.%s", template.query, iface["oid"]))
					snmpVars = append(snmpVars, Iface2SNMP{ifaceID: iface["id"], snmpTemplate: template})
				}
			}
		}

		//
		// fetch data from device (max queries per request)
		//
	LOOP_PROCESS_IFACE:
		for from = 0; from < len(snmpQueries); from += *snmpMaxOids {

			if to = from + *snmpMaxOids; to > len(snmpQueries) {
				to = len(snmpQueries)
			}

			result, err := snmpInst.Get(snmpQueries[from:to])
			if err != nil {
				l.Printf(h.ERROR, "%s: host %s got emprty responce, error: %s", funcName, dev.ip, err)
				break LOOP_PROCESS_IFACE
			}

			for _, pdu := range result.Variables {
				valRRD := new(big.Float)
				if err = ParseSNMPResult(&pdu, valRRD); err != nil {
					l.Printf(h.ERROR, "%s: host %s, error: %s", funcName, dev.ip, err)
					continue LOOP_PROCESS_IFACE
				}
				snmpResp = append(snmpResp, valRRD)
			}
		}

		if err := RRDStoreValues(&snmpResp, &snmpVars); err != nil {
			l.Printf(h.ERROR, "%s: host %s, error: %s", funcName, dev.ip, err)
		}
	}

	l.Printf(h.FUNC, "Stop: %s - %d, diration: %d", funcName, time.Now().Unix(), time.Now().Unix()-start)
}
