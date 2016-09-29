package logmux

import (
	"errors"
	"net"
	"sync"
	"text/template"
	"time"

	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/logaggregator/client"
	"github.com/flynn/flynn/logaggregator/utils"
	"github.com/flynn/flynn/pkg/syslog/rfc6587"
	"gopkg.in/inconshreveable/log15.v2"
)

var SinkExistsError = errors.New("sink with that id already exists")
var SinkNotFoundError = errors.New("sink with that id couldn't be found")

type SinkManager struct {
	mux    *Mux
	logger log15.Logger
	sinks  map[string]Sink
}

func NewSinkManager(mux *Mux, logger log15.Logger) *SinkManager {
	return &SinkManager{
		mux:    mux,
		logger: logger,
		sinks:  make(map[string]Sink),
	}
}

func (sm *SinkManager) AddSink(id string, s Sink) error {
	if _, ok := sm.sinks[id]; ok {
		return SinkExistsError
	}
	sm.sinks[id] = s
	go sm.mux.addSink(s)
	return nil
}

func (sm *SinkManager) RemoveSink(id string) error {
	if s, ok := sm.sinks[id]; !ok {
		return SinkNotFoundError
	} else {
		s.Shutdown()
	}
	delete(sm.sinks, id)
	return nil
}

func (sm *SinkManager) StreamToAggregators(s discoverd.Service) error {
	log := sm.logger.New("fn", "StreamToAggregators")
	ch := make(chan *discoverd.Event)
	_, err := s.Watch(ch)
	if err != nil {
		log.Error("failed to connect to discoverd watch", "error", err)
		return err
	}
	log.Info("connected to discoverd watch")
	go func() {
		for e := range ch {
			switch e.Kind {
			case discoverd.EventKindUp:
				log.Info("connecting to new aggregator", "addr", e.Instance.Addr)
				s, err := NewLogAggregatorSink(e.Instance.Addr)
				if err != nil {
					log.Error("failed to connect to aggregator", "addr", e.Instance.Addr)
				}
				sm.AddSink(e.Instance.Addr, s)
			case discoverd.EventKindDown:
				sm.RemoveSink(e.Instance.Addr)
			}
		}
	}()
	return nil
}

type Sink interface {
	Connect() error
	Close()
	GetCursors() (map[string]utils.HostCursor, error)
	Write(m message) error
	Shutdown()
	ShutdownCh() chan struct{}
}

type LogAggregatorSink struct {
	addr             string
	conn             net.Conn
	aggregatorClient *client.Client

	shutdownOnce sync.Once
	shutdownCh   chan struct{}
}

func NewLogAggregatorSink(addr string) (*LogAggregatorSink, error) {
	return &LogAggregatorSink{addr: addr}, nil
}

func (s *LogAggregatorSink) Connect() error {
	// Connect TCP connection to aggregator
	// TODO(titanous): add dial timeout
	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		return err
	}
	// Connect to logaggregator HTTP endpoint
	host, _, _ := net.SplitHostPort(s.addr)
	c, err := client.New("http://" + host)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		return err
	}
	s.conn = conn
	s.aggregatorClient = c
	return nil
}

func (s *LogAggregatorSink) Close() {
	s.conn.Close()
}

func (s *LogAggregatorSink) GetCursors() (map[string]utils.HostCursor, error) {
	return s.aggregatorClient.GetCursors()
}

func (s *LogAggregatorSink) Write(m message) error {
	_, err := s.conn.Write(rfc6587.Bytes(m.Message))
	return err
}

func (s *LogAggregatorSink) Shutdown() {
	s.shutdownOnce.Do(func() { close(s.shutdownCh) })
}

func (s *LogAggregatorSink) ShutdownCh() chan struct{} {
	return s.shutdownCh
}

// TCPSink is a flexible sink that can connect to TCP/TLS endpoints and write log messages using
// a defined format string. It can be used to connect to rsyslog and similar services that accept TCP connections.
type TCPSink struct {
	addr            string
	template        string
	syncMaxTime     time.Time
	syncMaxMessages int32

	conn         net.Conn
	shutdownOnce sync.Once
	shutdownCh   chan struct{}
}

func NewTCPSink(addr string) (*TCPSink, error) {
	return &TCPSink{addr: addr}, nil
}

func (s *TCPSink) Connect() error {
	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		return err
	}
	s.conn = conn
	return nil
}

func (s *TCPSink) GetCursors() (map[string]utils.HostCursor, error) {
	// read cursors from disk
	return nil, nil
}

type Data struct {
	AppName string
}

func (s *TCPSink) Write(m message) error {
	// format the message using gofmt string provided
	// write message to socket
	// update local cursor, if past syncInterval or over syncMaxMessages then sync cursor to disk
	tmpl, err := template.New("").Parse(s.template)
	if err != nil {
		return err
	}
	data := Data{
		AppName: "test",
	}
	tmpl.Execute(s.conn, data)
	return nil
}

func (s *TCPSink) Close() {
	if s.conn != nil {
		s.conn.Close()
	}
}

func (s *TCPSink) Shutdown() {
	s.shutdownOnce.Do(func() { close(s.shutdownCh) })
}

func (s *TCPSink) ShutdownCh() chan struct{} {
	return s.shutdownCh
}
