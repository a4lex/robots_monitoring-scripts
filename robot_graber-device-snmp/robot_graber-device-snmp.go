package main

import (
	"fmt"
	"math/big"
	"regexp"
	"sync"
	"time"

	h "github.com/a4lex/go-helpers"
)

const (
	sqlGetDevice = `SELECT d.id, d.device_type_id, INET_NTOA(d.ip) AS ip, t.snmp_version, d.community ` +
		`FROM devices d, device_types t WHERE t.id=d.device_type_id AND d.monitor=1`
	sqlGetSnmpTemplates = `SELECT device_type_id, CONCAT(t.shared, '/', t.name) AS path, t.query, t.rate, t.type AS couter_type, t.min, t.max, t.step, t.threshold  ` +
		`FROM snmp_templates t, device_types d, device_type_snmp_template dt WHERE d.id=dt.device_type_id AND t.id=dt.snmp_template_id`
)

var snmpTemplates map[string][]*OID

func process() {

	wgQueryQueue := &sync.WaitGroup{}
	chanQuery := mysqli.InitQueryQueue(wgQueryQueue)

	timeUpdRRD = time.Now()
	l.Printf(h.DEBUG, "Time for RRD DB update fixed to: %s", timeUpdRRD.Format("2006-01-02 15:04:05"))

	//
	// Select SMMP templates and prepare them for use
	//

	snmpTemplates = make(map[string][]*OID)
	for _, row := range mysqli.DBSelectList(sqlGetSnmpTemplates) {

		if _, ok := snmpTemplates[row["device_type_id"]]; !ok {
			snmpTemplates[row["device_type_id"]] = []*OID{
				CreateSnmpTemplate(row),
			}
		} else {
			snmpTemplates[row["device_type_id"]] = append(
				snmpTemplates[row["device_type_id"]],
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
		go grabeDevice(wgDeviceQueue, i, chanQuery, deviceChannel)
	}

	//
	// Main Loop
	//
	listDevice := mysqli.DBSelectList(sqlGetDevice)
	for _, dev := range listDevice {
		deviceChannel <- &Device{dev["id"], dev["device_type_id"], dev["ip"], dev["snmp_version"], dev["community"]}
	}

	close(deviceChannel)
	wgDeviceQueue.Wait()

	close(chanQuery)
	wgQueryQueue.Wait()
}

func grabeDevice(wg *sync.WaitGroup, num int, chanQuery chan string, deviceChannel chan *Device) {
	defer wg.Done()
	funcName := fmt.Sprintf("grabeDevice[%d]", num)
	start := time.Now().Unix()
	l.Printf(h.FUNC, "Start: %s", funcName)

	//
	// Init helper vars
	//
	from, to, oidCount := 0, 0, 0

	snmpResp := make([]*big.Float, 0)
	snmpVars := make([]Iface2SNMP, 0)
	snmpQueries := make([]string, 0)

LOOP_PROCESS_DEVICE:
	for dev := range deviceChannel {

		regForNext := regexp.MustCompile(`^(.+)\.([0-9]+?)?$`)
		from, to, oidCount = 0, 0, len(snmpTemplates[dev.devType])
		snmpQueries = snmpQueries[:0]
		snmpVars = snmpVars[:0]
		snmpResp = snmpResp[:0]

		snmpInst := GetSnmpCon(dev.ip, dev.snmpVer, dev.community)
		if err := snmpInst.Connect(); err != nil {
			l.Printf(h.INFO, "%s: Host %s got connect error: %v", funcName, dev.ip, err)
			continue LOOP_PROCESS_DEVICE
		}
		defer snmpInst.Conn.Close()

		l.Printf(h.DEBUG, "%s: host %s, devType: %s, community: %s, snmpVer: %s, oidCount: %d",
			funcName, dev.ip, dev.devType, dev.community, dev.snmpVer, oidCount)

		for from = 0; from < oidCount; from += *snmpMaxOids {
			if to = from + *snmpMaxOids; to > oidCount {
				to = oidCount
			}

			var snmpQueries []string
			for _, template := range snmpTemplates[dev.devType][from:to] {
				snmpQueries = append(snmpQueries, regForNext.ReplaceAllString(template.query, "${1}"))
				snmpVars = append(snmpVars, Iface2SNMP{ifaceID: dev.id, snmpTemplate: template})
			}

			if result, err := snmpInst.GetNext(snmpQueries); err != nil {
				l.Printf(h.ERROR, "%s: host %s do not responce on GET request, error: %v", funcName, dev.ip, err)
				continue LOOP_PROCESS_DEVICE
			} else {
				for _, pdu := range result.Variables {
					valRRD := new(big.Float)
					if err = ParseSNMPResult(&pdu, valRRD); err != nil {
						l.Printf(h.ERROR, "%s: host %s, error: %s", funcName, dev.ip, err)
						valRRD = nil
					}
					l.Printf(h.DEBUG, "%s: host %s, got: %s - %s", funcName, dev.ip, pdu.Name, fmt.Sprintf("%.0f", valRRD))
					snmpResp = append(snmpResp, valRRD)
				}
			}
		}

		if err := RRDStoreValues(&snmpResp, &snmpVars); err != nil {
			l.Printf(h.ERROR, "%s: host %s, error: %s", funcName, dev.ip, err)
		} else {
			l.Printf(h.DEBUG, "%s: host %s, success updated %d values", funcName, dev.ip, len(snmpVars))
		}
	}

	l.Printf(h.FUNC, "Stop: %s - %d, diration: %d", funcName, time.Now().Unix(), time.Now().Unix()-start)
}
