package main

import (
	"flag"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"time"

	h "github.com/a4lex/go-helpers"
	"github.com/gosnmp/gosnmp"
)

//
// Device struct for store devicie info
//
type Device struct {
	id        string
	devType   string
	ip        string
	snmpVer   string
	community string
}

//
// OID strunc for store OIDs by dev_type
//
type OID struct {
	rrdDir     string
	query      string
	rate       *big.Float
	couterType string
	min        int
	max        int
	step       uint
}

//
// Iface2SNMP struct for store link ifID/snmpTemplate <-> snmpResponce
//
type Iface2SNMP struct {
	ifaceID      string
	snmpTemplate *OID
}

var (
	snmpRetries = flag.Int("snmp-retries", 1, "SNMP Retries connect to device")
	snmpTimeout = flag.Int("snmp-timeout", 3, "SNMP Timeout for waiting responce from device")
	snmpMaxOids = flag.Int("maxOID", 20, "SNMP Max OID count per request")
)

//
// GetSnmpCon - return new snmp connection to device
//
func GetSnmpCon(ip, snmpVer, snmpRO string) *gosnmp.GoSNMP {

	version := gosnmp.Version2c
	switch snmpVer {
	case "v1":
		version = gosnmp.Version1
	case "v2c":
		version = gosnmp.Version2c
	case "v3":
		version = gosnmp.Version3

	}
	return &gosnmp.GoSNMP{
		Target:             ip,
		Port:               161,
		Community:          snmpRO,
		MaxOids:            *snmpMaxOids,
		Retries:            *snmpRetries,
		Version:            version,
		Timeout:            time.Duration(time.Duration(*snmpTimeout) * time.Second),
		ExponentialTimeout: true,
	}
}

//
// CreateSnmpTemplate Return link on new SNMP template, created from map
//
func CreateSnmpTemplate(t map[string]string) *OID {
	rate := new(big.Float)
	rate, _ = rate.SetString(t["rate"])
	min, _ := strconv.Atoi(t["min"])
	max, _ := strconv.Atoi(t["max"])
	step, _ := strconv.ParseUint(t["step"], 10, 32)

	// create dir for rrd files, for current template
	rrdDirTemplate := fmt.Sprintf("%s/%s", *dirRRD, t["path"])
	if _, err := os.Stat(rrdDirTemplate); os.IsNotExist(err) {
		os.MkdirAll(rrdDirTemplate, os.ModePerm)
	}

	return &OID{
		rrdDir:     rrdDirTemplate,
		query:      t["query"],
		couterType: t["couter_type"],
		rate:       rate,
		min:        min,
		max:        max,
		step:       uint(step),
	}
}

//
// ParseSNMPResult - update value of givel *val
//
func ParseSNMPResult(pdu *gosnmp.SnmpPDU, val *big.Float) error {
	switch pdu.Type {
	case gosnmp.OctetString:
		if _, ok := val.SetString(string(pdu.Value.([]byte))); !ok {
			return fmt.Errorf("failed parse string responce, %s - resp: %s", pdu.Name, string(pdu.Value.([]byte)))
		}
	default:
		val.SetInt(gosnmp.ToBigInt(pdu.Value))
	}

	return nil
}

//
// RRDStoreValues - store all fetched values into RRD files
//
func RRDStoreValues(snmpResp *[]*big.Float, snmpVars *[]Iface2SNMP) error {

	if len(*snmpResp) != len(*snmpVars) {
		return fmt.Errorf("parsed responce count not match requested values count")
	}

	var fileRRD, ifaceID string
	var rrdTemplate *OID

LOOP_THROUGH_VAR:
	for id, valRRD := range *snmpResp {
		if valRRD == nil {
			continue LOOP_THROUGH_VAR
		}

		ifaceID = (*snmpVars)[id].ifaceID
		rrdTemplate = (*snmpVars)[id].snmpTemplate

		valRRD = valRRD.Mul(valRRD, rrdTemplate.rate)
		fileRRD = fmt.Sprintf("%s/%010s", rrdTemplate.rrdDir, ifaceID)

		if err := RRDUpdate(fileRRD, timeUpdRRD, fmt.Sprintf("%.0f", valRRD)); err != nil {
			if _, err := os.Stat(fileRRD); !os.IsNotExist(err) {
				continue LOOP_THROUGH_VAR
			}
			if _, err := RRDCreate(fileRRD, rrdTemplate.couterType, rrdTemplate.min, rrdTemplate.max, rrdTemplate.step); err != nil {
				l.Printf(h.ERROR, "Can not create rrddb: %s - %s", fileRRD, err)
			}
		}
	}

	return nil
}
