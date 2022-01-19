# robots_monitoring-scripts
Monitoring scripts for dipt

## Installing github.com/ziutek/rrd for RRD functions

rrd currently supports rrdtool-1.4.x

Install rrd with:

	dnf groupinstall "Development Tools"
	dnf install rrdtool-devel
	go get github.com/ziutek/rrd
