package main

import (
	"fmt"
	"regexp"
	"time"

	"github.com/a4lex/go-telnet"
)

type MyTelnet struct {
	*telnet.Conn
	connTimeout   time.Duration
	logger        func(string)
	comChainState bool
	lineData      *[]byte
}

func MyConnect(network, addr string, connTimeout time.Duration, logger func(string)) (*MyTelnet, error) {
	if t, err := telnet.Dial(network, addr); err != nil {
		return nil, err
	} else {
		return &MyTelnet{t, connTimeout, logger, true, &[]byte{}}, nil
	}
}

func (t MyTelnet) ResetCommandChainState() *MyTelnet {
	t.comChainState = true
	return &t
}

func (t MyTelnet) GetCommandChainState() bool {
	return t.comChainState
}

func (t MyTelnet) Expect(d ...string) *MyTelnet {
	if t.comChainState && t.isSuccess(t.SetReadDeadline(time.Now().Add(t.connTimeout))) {
		t.isSuccess(t.SkipUntil(d...))
	}
	return &t
}

func (t MyTelnet) SendLine(s string) *MyTelnet {
	if t.comChainState && t.isSuccess(t.SetWriteDeadline(time.Now().Add(connTimeout))) {
		buf := make([]byte, len(s)+1)
		copy(buf, s)
		buf[len(s)] = '\n'
		_, err := t.Write(buf)
		t.isSuccess(err)
	}
	return &t
}

func (t MyTelnet) ReadUntil(delim byte) *MyTelnet {
	if t.comChainState {
		if data, err := t.ReadBytes(delim); t.isSuccess(err) {
			t.lineData = &data
		}
	}
	return &t
}

func (t MyTelnet) SplitResponceByRegEx(re *regexp.Regexp) [][]string {
	if t.comChainState {
		return re.FindAllStringSubmatch(string(*t.lineData), -1)
	}
	return [][]string{}
}

func (t MyTelnet) isSuccess(err error) bool {
	if err != nil {
		t.logger(fmt.Sprintf("%s", err))
		t.comChainState = false
	}
	return t.comChainState
}
