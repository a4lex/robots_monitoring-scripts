package main

import (
	"flag"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"time"

	h "github.com/a4lex/go-helpers"
)

const (
	sqlGetQuery = `SELECT query, rate, type, step, min, max, threshold, CONCAT(shared, '/', name) AS path FROM snmp_templates WHERE source='mysql'`
)

var (
	delay        = flag.Int("delay", 0, "Delay before run program")
	maxPerUpdate = flag.Int("max-to-select", 10000, "Count of values for select from DB to update")
)

func process() {

	//
	// Sleep before start - another programs should do own tasks
	//
	time.Sleep(time.Duration(*delay) * time.Second)

	//
	// Main Loop
	//

	timeUpdRRD := time.Now().Add(time.Duration(-(*delay)) * time.Second)
	listSNMPTemplates := mysqli.DBSelectList(sqlGetQuery)
	var listVals []map[string]string

	for _, template := range listSNMPTemplates {

		// create dir for rrd files, for current template
		rrdDirTemplate := fmt.Sprintf("%s/%s", *dirRRD, template["path"])
		if _, err := os.Stat(rrdDirTemplate); os.IsNotExist(err) {
			os.MkdirAll(rrdDirTemplate, os.ModePerm)
			l.Printf(h.INFO, "Create dir for RRD files: %s", rrdDirTemplate)
		}

		rate := new(big.Float)
		rate, _ = rate.SetString(template["rate"])
		min, _ := strconv.Atoi(template["min"])
		max, _ := strconv.Atoi(template["max"])
		step, _ := strconv.ParseUint(template["step"], 10, 32)

		// loop by values of template
		// select not all values, pick just part
		// of it every iteration of the loop
		offset := 0
		for {
			listVals = mysqli.DBSelectList(fmt.Sprintf("%s LIMIT %d, %d", template["query"], offset, *maxPerUpdate))
			for _, val := range listVals {

				// store value in rrd db or create it
				fileRRD := fmt.Sprintf("%s/%s", rrdDirTemplate, val["file"])

				valRRD := new(big.Float)
				valRRD.SetString(val["val"])
				valRRD = valRRD.Mul(valRRD, rate)

				if err := RRDUpdate(fileRRD, timeUpdRRD, fmt.Sprintf("%.0f", valRRD)); err != nil {
					if _, err := os.Stat(fileRRD); os.IsExist(err) {
						continue
					}
					if _, err := RRDCreate(fileRRD, template["type"], min, max, uint(step)); err != nil {
						l.Printf(h.ERROR, "Can not create rrddb: %s - %s", val["path"], err)
					}
				}
			}

			if len(listVals) <= *maxPerUpdate {
				break
			}
			offset += *maxPerUpdate
		}

	}

	l.Printf(h.FUNC, "END")
}
