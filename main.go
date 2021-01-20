package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	h "github.com/a4lex/go-helpers"
)

var (
	l      h.MyLogger
	mysqli h.MySQL
	cfg    h.Config

	timeUpdRRD time.Time

	configPath = flag.String("configs-file", "config.yml", "Configs path")
	logPath    = flag.String("log-file", fmt.Sprintf("%s.log", os.Args[0]), "Log path")
	logVerbose = flag.Int("log-level", 63, "Log verbose [ 1 - Fatal, 2 - ERROR, 4 - INFO, 8 - MYSQL, 16 - FUNC, 32 - DEBUG ]")

	dirRRD      = flag.String("dir-rrd", "/tmp/rrd", "Path to store RRD files")
	threadCount = flag.Int("thread", 1, "Thread count for processing")
)

func main() {
	flag.Parse()

	finish := make(chan os.Signal, 1)
	signal.Notify(finish, syscall.SIGINT, syscall.SIGTERM)

	//
	// Init logs
	//

	f, err := os.OpenFile(*logPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		panic(fmt.Sprintf("error opening file: %v", err))
	}
	defer f.Close()

	l = h.InitLog(f, *logVerbose)
	l.Printf(h.FUNC, "START")

	//
	// Load config vars
	//

	if err = h.LoadConfig(*configPath, &cfg); err != nil {
		panic(fmt.Sprintf("can not init config: %v", err))
	}

	//
	// Init MySQL connection
	//

	mysqli, err = h.DBConnect(&l, cfg.Database.Host, cfg.Database.User, cfg.Database.Pass, cfg.Database.Database, cfg.Database.DSN, cfg.Database.MaxIdle, cfg.Database.MaxOpen)
	if err != nil {
		panic(fmt.Sprintf("failed connect to DB: %v", err))
	}

	process()

	l.Printf(h.FUNC, "END")
}

func isFlagPassed(name string) (found bool) {
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

//
// RRDCreate - create file of RRD DB
//
func RRDCreate(dbfile, counterType string, min, max int, step uint) (string, error) {

	return "nil", nil
}

//
// RRDUpdate - update RRD DB file with given value and time
//
func RRDUpdate(dbfile string, time time.Time, val string) error {
	return nil
}

// //
// // RRDCreate - create file of RRD DB
// //
// func RRDCreate(dbfile, counterType string, min, max int, step uint) (*rrd.Creator, error) {

// 	c := rrd.NewCreator(dbfile, time.Now(), step)
// 	c.DS("val", counterType, step*2, min, max)
// 	c.RRA("AVERAGE", 0.5, 1, 288)
// 	c.RRA("LAST", 0.5, 1, 288)
// 	c.RRA("MIN", 0.5, 1, 288)
// 	c.RRA("MAX", 0.5, 1, 288)
// 	c.RRA("AVERAGE", 0.5, 7, 288)
// 	c.RRA("LAST", 0.5, 7, 288)
// 	c.RRA("MIN", 0.5, 7, 288)
// 	c.RRA("MAX", 0.5, 7, 288)
// 	c.RRA("AVERAGE", 0.5, 30, 288)
// 	c.RRA("LAST", 0.5, 30, 288)
// 	c.RRA("MIN", 0.5, 30, 288)
// 	c.RRA("MAX", 0.5, 30, 288)
// 	c.RRA("AVERAGE", 0.5, 365, 288)
// 	c.RRA("LAST", 0.5, 365, 288)
// 	c.RRA("MIN", 0.5, 365, 288)
// 	c.RRA("MAX", 0.5, 365, 288)

// 	if err := c.Create(true); err != nil {
// 		l.Printf(h.ERROR, "Can not create RRD DB: %s, counterType: %s, min: %d, max: %d", dbfile, counterType, min, max)
// 		return nil, err
// 	} else {
// 		l.Printf(h.DEBUG, "Create RRD DB: %s, counterType: %s, min: %d, max: %d", dbfile, counterType, min, max)
// 		return c, nil
// 	}
// }

// //
// // RRDUpdate - update RRD DB file with given value and time
// //
// func RRDUpdate(dbfile string, time time.Time, val string) error {
// 	u := rrd.NewUpdater(dbfile)
// 	if err := u.Update(time, val); err != nil {
// 		l.Printf(h.DEBUG, "Update is failed RRD DB: %s, time: %s, val: %s, error: %s", dbfile, time.Format("2006-01-02 15:04:05"), val, err)
// 		return err
// 	}
// 	l.Printf(h.DEBUG, "Update RRD DB: %s, time: %s, val: %s", dbfile, time.Format("2006-01-02 15:04:05"), val)
// 	return nil
// }
