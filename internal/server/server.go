package server

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/musix/backhaul/internal/config"
	"github.com/musix/backhaul/internal/server/transport"
	"github.com/musix/backhaul/internal/utils"

	"github.com/sirupsen/logrus"
)

type Server struct {
	config *config.ServerConfig
	ctx    context.Context
	cancel context.CancelFunc
	logger *logrus.Logger
}

func NewServer(cfg *config.ServerConfig, parentCtx context.Context) *Server {
	ctx, cancel := context.WithCancel(parentCtx)
	return &Server{
		config: cfg,
		ctx:    ctx,
		cancel: cancel,
		logger: utils.NewLogger(cfg.LogLevel),
	}
}

// ensureIPSetExists checks if the ipset exists, and creates it if not
func ensureIPSetExists(ipsetName string) error {
	cmd := exec.Command("ipset", "list", ipsetName)
	if err := cmd.Run(); err != nil {
		// Create the ipset if it doesn't exist
		log.Printf("ipset %s not found, creating it...", ipsetName)
		cmd = exec.Command("ipset", "create", ipsetName, "hash:net")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create ipset %s: %w", ipsetName, err)
		}
	}
	return nil
}

// addCIDRToIPSet adds a CIDR to the specified ipset, handling duplicates gracefully
func addCIDRToIPSet(ipsetName, cidr string) error {
	cmd := exec.Command("ipset", "add", ipsetName, cidr)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if the error is due to the CIDR already being added
		if strings.Contains(string(output), "already added") {
			return nil // Ignore "already added" errors
		}
		return fmt.Errorf("failed to add CIDR %s to ipset %s: %v, output: %s", cidr, ipsetName, err, string(output))
	}
	return nil
}

// processCIDRs processes the CIDR blocks from a file and adds them to the ipset
func processCIDRs(fileName, ipsetName string, knownCIDRs *sync.Map) error {
	// Open the file
	file, err := os.Open(fileName)
	if err != nil {
		return fmt.Errorf("could not open file %s: %w", fileName, err)
	}
	defer file.Close()

	// Read the file line by line
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip blank or invalid lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, _, err := net.ParseCIDR(line); err != nil {
			log.Printf("Skipping invalid CIDR: %s\n", line)
			continue
		}

		// Check if the CIDR is already known
		if _, exists := knownCIDRs.Load(line); exists {
			continue
		}

		// Add valid CIDR to the ipset
		if err := addCIDRToIPSet(ipsetName, line); err != nil {
			log.Printf("Error adding CIDR %s to ipset %s: %v\n", line, ipsetName, err)
			continue
		}

		// Mark the CIDR as known
		knownCIDRs.Store(line, true)
	}

	// Check for file reading errors
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading file %s: %w", fileName, err)
	}

	return nil
}

// watchFile watches the file for changes and updates the ipset
func watchFile(fileName, ipsetName string, knownCIDRs *sync.Map) {
	var lastModTime time.Time

	for {
		// Check the file's last modification time
		info, err := os.Stat(fileName)
		if err != nil {
			log.Printf("Error checking file %s: %v\n", fileName, err)
			time.Sleep(10 * time.Second)
			continue
		}

		// If the file has been modified since the last check, process it
		if info.ModTime().After(lastModTime) {
			log.Println("File has changed. Processing...")
			if err := processCIDRs(fileName, ipsetName, knownCIDRs); err != nil {
				log.Printf("Error processing file: %v\n", err)
			} else {
				lastModTime = info.ModTime()
			}
		}

		// Wait for 10 seconds before checking again
		time.Sleep(10 * time.Second)
	}
}

func checkIPSetAvailability() {
	// Check if ipset is available
	_, err := exec.LookPath("ipset")
	if err != nil {
		log.Println("Error: ipset is not available on this system. Please install ipset and try again.")
		log.Fatal(err) // Exit the application with an error
	}
	log.Println("ipset is available.")
}

func (s *Server) Start() {

	if len(s.config.CIDR) > 0 {

		checkIPSetAvailability()

		fileName := s.config.CIDR
		ipsetName := "allowedcidr"
		knownCIDRs := &sync.Map{}

		// Ensure the ipset exists
		if err := ensureIPSetExists(ipsetName); err != nil {
			log.Fatalf("Failed to ensure ipset exists: %v", err)
		}

		// Initialize known CIDRs from the file on startup
		log.Println("Initializing known CIDRs...")
		if err := processCIDRs(fileName, ipsetName, knownCIDRs); err != nil {
			log.Fatalf("Failed to initialize CIDRs: %v", err)
		}

		// Start watching the file for changes
		log.Println("Watching file for changes...")
		go watchFile(fileName, ipsetName, knownCIDRs)
	}

	// for pprof and debugging
	if s.config.PPROF {
		go func() {
			s.logger.Info("pprof started at port 6060")
			http.ListenAndServe("0.0.0.0:6060", nil)
		}()
	}

	if s.config.Transport == config.TCP {
		tcpConfig := &transport.TcpConfig{
			BindAddr:    s.config.BindAddr,
			Nodelay:     s.config.Nodelay,
			KeepAlive:   time.Duration(s.config.Keepalive) * time.Second,
			Heartbeat:   time.Duration(s.config.Heartbeat) * time.Second,
			Token:       s.config.Token,
			ChannelSize: s.config.ChannelSize,
			Ports:       s.config.Ports,
			Sniffer:     s.config.Sniffer,
			WebPort:     s.config.WebPort,
			SnifferLog:  s.config.SnifferLog,
			AcceptUDP:   s.config.AcceptUDP,
			Matchers:    s.config.Matchers,
		}

		tcpServer := transport.NewTCPServer(s.ctx, tcpConfig, s.logger)
		go tcpServer.Start()

	} else if s.config.Transport == config.TCPMUX {
		tcpMuxConfig := &transport.TcpMuxConfig{
			BindAddr:         s.config.BindAddr,
			Nodelay:          s.config.Nodelay,
			KeepAlive:        time.Duration(s.config.Keepalive) * time.Second,
			Heartbeat:        time.Duration(s.config.Heartbeat) * time.Second,
			Token:            s.config.Token,
			ChannelSize:      s.config.ChannelSize,
			Ports:            s.config.Ports,
			MuxCon:           s.config.MuxCon,
			MuxVersion:       s.config.MuxVersion,
			MaxFrameSize:     s.config.MaxFrameSize,
			MaxReceiveBuffer: s.config.MaxReceiveBuffer,
			MaxStreamBuffer:  s.config.MaxStreamBuffer,
			Sniffer:          s.config.Sniffer,
			WebPort:          s.config.WebPort,
			SnifferLog:       s.config.SnifferLog,
			Matchers:         s.config.Matchers,
		}

		tcpMuxServer := transport.NewTcpMuxServer(s.ctx, tcpMuxConfig, s.logger)
		go tcpMuxServer.Start()
	} else if s.config.Transport == config.TCPUMUX {
		tcpUMuxConfig := &transport.TcpUMuxConfig{
			BindAddr:         s.config.BindAddr,
			Nodelay:          s.config.Nodelay,
			KeepAlive:        time.Duration(s.config.Keepalive) * time.Second,
			Heartbeat:        time.Duration(s.config.Heartbeat) * time.Second,
			Token:            s.config.Token,
			ChannelSize:      s.config.ChannelSize,
			Ports:            s.config.Ports,
			MuxCon:           s.config.MuxCon,
			MuxVersion:       s.config.MuxVersion,
			MaxFrameSize:     s.config.MaxFrameSize,
			MaxReceiveBuffer: s.config.MaxReceiveBuffer,
			MaxStreamBuffer:  s.config.MaxStreamBuffer,
			Sniffer:          s.config.Sniffer,
			WebPort:          s.config.WebPort,
			SnifferLog:       s.config.SnifferLog,
			Matchers:         s.config.Matchers,
		}

		tcpUMuxServer := transport.NewTcpUMuxServer(s.ctx, tcpUMuxConfig, s.logger)
		go tcpUMuxServer.Start()

	} else if s.config.Transport == config.WS || s.config.Transport == config.WSS {
		wsConfig := &transport.WsConfig{
			BindAddr:    s.config.BindAddr,
			Nodelay:     s.config.Nodelay,
			KeepAlive:   time.Duration(s.config.Keepalive) * time.Second,
			Heartbeat:   time.Duration(s.config.Heartbeat) * time.Second,
			Token:       s.config.Token,
			ChannelSize: s.config.ChannelSize,
			Ports:       s.config.Ports,
			Sniffer:     s.config.Sniffer,
			WebPort:     s.config.WebPort,
			SnifferLog:  s.config.SnifferLog,
			Mode:        s.config.Transport,
			TLSCertFile: s.config.TLSCertFile,
			TLSKeyFile:  s.config.TLSKeyFile,
			Matchers:    s.config.Matchers,
		}

		wsServer := transport.NewWSServer(s.ctx, wsConfig, s.logger)
		go wsServer.Start()

	} else if s.config.Transport == config.WSMUX || s.config.Transport == config.WSSMUX {
		wsMuxConfig := &transport.WsMuxConfig{
			BindAddr:         s.config.BindAddr,
			Nodelay:          s.config.Nodelay,
			KeepAlive:        time.Duration(s.config.Keepalive) * time.Second,
			Heartbeat:        time.Duration(s.config.Heartbeat) * time.Second,
			Token:            s.config.Token,
			ChannelSize:      s.config.ChannelSize,
			Ports:            s.config.Ports,
			MuxCon:           s.config.MuxCon,
			MuxVersion:       s.config.MuxVersion,
			MaxFrameSize:     s.config.MaxFrameSize,
			MaxReceiveBuffer: s.config.MaxReceiveBuffer,
			MaxStreamBuffer:  s.config.MaxStreamBuffer,
			Sniffer:          s.config.Sniffer,
			WebPort:          s.config.WebPort,
			SnifferLog:       s.config.SnifferLog,
			Mode:             s.config.Transport,
			TLSCertFile:      s.config.TLSCertFile,
			TLSKeyFile:       s.config.TLSKeyFile,
			Matchers:         s.config.Matchers,
		}

		wsMuxServer := transport.NewWSMuxServer(s.ctx, wsMuxConfig, s.logger)
		go wsMuxServer.Start()

	} else if s.config.Transport == config.QUIC {
		quicConfig := &transport.QuicConfig{
			BindAddr:    s.config.BindAddr,
			Nodelay:     s.config.Nodelay,
			KeepAlive:   time.Duration(s.config.Keepalive) * time.Second,
			Heartbeat:   time.Duration(s.config.Heartbeat) * time.Second,
			Token:       s.config.Token,
			MuxCon:      s.config.MuxCon,
			ChannelSize: s.config.ChannelSize,
			Ports:       s.config.Ports,
			Sniffer:     s.config.Sniffer,
			WebPort:     s.config.WebPort,
			SnifferLog:  s.config.SnifferLog,
			TLSCertFile: s.config.TLSCertFile,
			TLSKeyFile:  s.config.TLSKeyFile,
		}

		quicServer := transport.NewQuicServer(s.ctx, quicConfig, s.logger)
		go quicServer.TunnelListener()

	} else if s.config.Transport == config.UDP {
		udpConfig := &transport.UdpConfig{
			BindAddr:    s.config.BindAddr,
			Heartbeat:   time.Duration(s.config.Heartbeat) * time.Second,
			Token:       s.config.Token,
			ChannelSize: s.config.ChannelSize,
			Ports:       s.config.Ports,
			Sniffer:     s.config.Sniffer,
			WebPort:     s.config.WebPort,
			SnifferLog:  s.config.SnifferLog,
		}

		udpServer := transport.NewUDPServer(s.ctx, udpConfig, s.logger)
		go udpServer.Start()

	} else {
		s.logger.Fatal("invalid transport type: ", s.config.Transport)
	}

	<-s.ctx.Done()

	s.logger.Info("all workers stopped successfully")

	// supress other logs
	s.logger.SetLevel(logrus.FatalLevel)
}

// Stop shuts down the server gracefully
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}
