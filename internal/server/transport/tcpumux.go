package transport

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/musix/backhaul/internal/utils"
	"github.com/musix/backhaul/internal/web"

	"github.com/sirupsen/logrus"
	"github.com/xtaci/smux"
)

var userChannelSize int = 300

type UserTracker struct {
	IP                 string
	userSession        *smux.Session
	userLocalChannel   chan LocalTCPConn
	localChannelClosed bool
	onceHandleUser     sync.Once
	onceTrackSession   sync.Once
	ctx                context.Context
	cancel             context.CancelFunc
	mutex              sync.Mutex
}

func NewUserTracker(IP string) *UserTracker {
	ctx, cancel := context.WithCancel(context.Background())
	return &UserTracker{
		IP:                 IP,
		userSession:        nil,
		userLocalChannel:   make(chan LocalTCPConn, userChannelSize),
		localChannelClosed: false,
		ctx:                ctx,
		cancel:             cancel,
	}
}

func (ut *UserTracker) TrackSessionStreams(s *TcpUMuxTransport) {
	//check every second for 300 second, if we have no open stream for 300 seconds

	tickerTime := 10 * time.Second
	ticker := time.NewTicker(tickerTime)
	defer ticker.Stop()
	//این فانکشن مهمیه. چون هم وضیفه داره کلیر کنه سشن کاربر رو هم وظیفه داره که فانکشن های مربوط رو خارج کنه

	s.logger.Debugf("start tracking sessions for ip %s", ut.IP)
	var timeNum int = 0

	var timeNil int = 0

	for {
		select {
		case <-ut.ctx.Done():
			return

		case <-ticker.C:
			s.logger.Debugf("user %s has session:", ut.IP, ut.userSession.NumStreams(), "which is closed?:", ut.userSession.IsClosed())

			if ut.userSession != nil && ut.userSession.NumStreams() == 0 {
				timeNum += 1
				s.logger.Debugf("there is no streams for more than", timeNum*5)

				if timeNum > 3 {
					ut.cancel()
					s.logger.Debugf("closed because no more than", timeNum*5)
					//ut.userSession.Close()

					var sssesion *smux.Session

					s.UsersMapMutex.Lock()
					sssesion = ut.userSession
					delete(s.UsersMap, ut.IP)
					s.UsersMapMutex.Unlock()
					s.logger.Warn("we took session for ip", ut.IP)

					if sssesion != nil {
						s.logger.Warn("EXPERIMENTAL instead of closing userSession we let other users use it!")
						select {
						case s.tunnelChannel <- sssesion:
						case <-time.After(time.Second * 1):
							s.logger.Warn("tunnel channel was filled, because of that we cannot put the session back to tunnel channel")
							sssesion.Close() //because tunnelchannel does not received our session we closed it!
						}
					}
					return
				}
				if ut.userSession != nil && ut.userSession.NumStreams() > 0 {
					timeNum = 0
					s.logger.Debugf("because we had streams more than 0 we zeroed timenum")
				}

				if ut.userSession == nil {
					timeNil += 1

					if timeNil > 3 {
						ut.cancel()
						s.logger.Debugf("closed because no more than", timeNil*5)
						s.UsersMapMutex.Lock()
						delete(s.UsersMap, ut.IP)
						s.UsersMapMutex.Unlock()
						s.logger.Warn("we took userMap for ip", ut.IP, "because user session was NIL for 30 secs?")
						return
					}
				} else {
					timeNil = 0
				}
			}

			//NO WE DON'T NEED THIS PART BECAUSE IF IT GOT CLOSED WE PULL A NEW SESSION THAT IT WILL BE CLOSED
			//BECAUSE OF NO STREAMS ON IT
			//if ut.userSession != nil && ut.userSession.IsClosed() {
			//session is closed already, we should renew a session or clear it!
			//we close it because it may got closed because of timeout or something else?
			//}

		}
	}
}

type TcpUMuxTransport struct {
	config           *TcpUMuxConfig
	smuxConfig       *smux.Config
	parentctx        context.Context
	ctx              context.Context
	cancel           context.CancelFunc
	logger           *logrus.Logger
	tunnelChannel    chan *smux.Session
	handshakeChannel chan net.Conn
	localChannel     chan LocalTCPConn
	reqNewConnChan   chan struct{}
	controlChannel   net.Conn
	usageMonitor     *web.Usage
	UsersMap         map[string]*UserTracker
	UsersMapMutex    sync.Mutex
	restartMutex     sync.Mutex
	Matchers         []string
	PortAndMatchers  map[int][]string
}

type TcpUMuxConfig struct {
	BindAddr         string
	TunnelStatus     string
	SnifferLog       string
	Token            string
	Ports            []string
	Nodelay          bool
	Sniffer          bool
	ChannelSize      int
	MuxCon           int
	MuxVersion       int
	MaxFrameSize     int
	MaxReceiveBuffer int
	MaxStreamBuffer  int
	WebPort          int
	KeepAlive        time.Duration
	Heartbeat        time.Duration // in seconds
	Matchers         []string
}

func NewTcpUMuxServer(parentCtx context.Context, config *TcpUMuxConfig, logger *logrus.Logger) *TcpUMuxTransport {
	// Create a derived context from the parent context
	ctx, cancel := context.WithCancel(parentCtx)

	// Initialize the TcpTransport struct
	server := &TcpUMuxTransport{
		smuxConfig: &smux.Config{
			Version:           config.MuxVersion,
			KeepAliveInterval: 20 * time.Second,
			KeepAliveTimeout:  40 * time.Second,
			KeepAliveDisabled: false,
			MaxFrameSize:      config.MaxFrameSize,
			MaxReceiveBuffer:  config.MaxReceiveBuffer,
			MaxStreamBuffer:   config.MaxStreamBuffer,
		},
		config:           config,
		parentctx:        parentCtx,
		ctx:              ctx,
		cancel:           cancel,
		logger:           logger,
		tunnelChannel:    make(chan *smux.Session, config.ChannelSize),
		handshakeChannel: make(chan net.Conn),
		localChannel:     make(chan LocalTCPConn, config.ChannelSize),
		reqNewConnChan:   make(chan struct{}, config.ChannelSize),
		controlChannel:   nil, // will be set when a control connection is established
		usageMonitor:     web.NewDataStore(fmt.Sprintf(":%v", config.WebPort), ctx, config.SnifferLog, config.Sniffer, &config.TunnelStatus, logger),
		PortAndMatchers:  make(map[int][]string),
		UsersMap:         make(map[string]*UserTracker),
	}

	return server
}

func (s *TcpUMuxTransport) Start() {
	if s.config.WebPort > 0 {
		go s.usageMonitor.Monitor()
	}
	s.config.TunnelStatus = "Disconnected (TCPUMux)"

	go s.tunnelListener()

	s.channelHandshake()

	if s.controlChannel != nil {
		s.config.TunnelStatus = "Connected (TCPUMux)"

		go s.parsePortMappings()
		go s.channelHandler()
		s.ParseMatchers()

	}

}
func (s *TcpUMuxTransport) Restart() {
	if !s.restartMutex.TryLock() {
		s.logger.Warn("server restart already in progress, skipping restart attempt")
		return
	}
	defer s.restartMutex.Unlock()

	s.logger.Info("restarting server...")
	if s.cancel != nil {
		s.cancel()
	}

	// for removing timeout logs
	level := s.logger.Level
	s.logger.SetLevel(logrus.FatalLevel)

	// Close any open connections in the tunnel channel.
	if s.controlChannel != nil {
		s.controlChannel.Close()
	}

	time.Sleep(2 * time.Second)

	ctx, cancel := context.WithCancel(s.parentctx)
	s.ctx = ctx
	s.cancel = cancel

	// Re-initialize variables
	s.tunnelChannel = make(chan *smux.Session, s.config.ChannelSize)
	s.localChannel = make(chan LocalTCPConn, s.config.ChannelSize)
	s.reqNewConnChan = make(chan struct{}, s.config.ChannelSize)
	s.handshakeChannel = make(chan net.Conn)
	s.controlChannel = nil
	s.usageMonitor = web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, &s.config.TunnelStatus, s.logger)
	s.config.TunnelStatus = ""
	s.UsersMap = make(map[string]*UserTracker)

	// set the log level again
	s.logger.SetLevel(level)

	go s.Start()
}

func (s *TcpUMuxTransport) channelHandshake() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case conn := <-s.handshakeChannel:
			// Set a read deadline for the token response
			if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				s.logger.Errorf("failed to set read deadline: %v", err)
				conn.Close()
				continue
			}
			msg, transport, err := utils.ReceiveBinaryTransportString(conn)
			if transport != utils.SG_Chan {
				s.logger.Errorf("invalid signal received for channel, Discarding connection")
				conn.Close()
				continue
			} else if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					s.logger.Warn("timeout while waiting for control channel signal")
				} else {
					s.logger.Errorf("failed to receive control channel signal: %v", err)
				}
				conn.Close() // Close connection on error or timeout
				continue
			}

			// Resetting the deadline (removes any existing deadline)
			conn.SetReadDeadline(time.Time{})

			if msg != s.config.Token {
				s.logger.Warnf("invalid security token received: %s", msg)
				conn.Close()
				continue
			}

			err = utils.SendBinaryTransportString(conn, s.config.Token, utils.SG_Chan)
			if err != nil {
				s.logger.Errorf("failed to send security token: %v", err)
				conn.Close()
				continue
			}

			//FORCE CONTROL CHANNEL TO BE TCP_NODELAY
			tcpConn, ok := conn.(*net.TCPConn)
			if !ok {
				conn.Close()
				continue
			}
			if err := tcpConn.SetNoDelay(true); err != nil {
				s.logger.Warnf("failed to set TCP_NODELAY for Control Channel %s: %v", tcpConn.RemoteAddr().String(), err)
			}

			s.controlChannel = conn

			s.logger.Info("control channel successfully established.")

			return
		}
	}
}

func (s *TcpUMuxTransport) channelHandler() {
	ticker := time.NewTicker(s.config.Heartbeat)
	defer ticker.Stop()

	// Channel to receive the message or error
	messageChan := make(chan byte, 1)

	go func() {
		message, err := utils.ReceiveBinaryByte(s.controlChannel)
		if err != nil {
			if s.cancel != nil {
				s.logger.Error("failed to read from channel connection. ", err)
				go s.Restart()
			}
			return
		}
		messageChan <- message
	}()

	for {
		select {
		case <-s.ctx.Done():
			_ = utils.SendBinaryByte(s.controlChannel, utils.SG_Closed)
			return

		case <-s.reqNewConnChan:
			err := utils.SendBinaryByte(s.controlChannel, utils.SG_Chan)
			if err != nil {
				s.logger.Error("failed to send request new connection signal. ", err)
				go s.Restart()
				return
			}

		case <-ticker.C:
			err := utils.SendBinaryByte(s.controlChannel, utils.SG_HB)
			if err != nil {
				s.logger.Error("failed to send heartbeat signal")
				go s.Restart()
				return
			}
			s.logger.Trace("heartbeat signal sent successfully")

		case message, ok := <-messageChan:
			if !ok {
				s.logger.Error("channel closed, likely due to an error in TCP read")
				return
			}

			if message == utils.SG_Closed {
				s.logger.Warn("control channel has been closed by the client")
				go s.Restart()
				return
			}
		}
	}
}

func (s *TcpUMuxTransport) tunnelListener() {
	//listener, err := net.Listen("tcp", s.config.BindAddr)
	listener, err := listenWithBuffers("tcp", s.config.BindAddr, 3*1024*1024, 3*1024*1024, 1320, "bbr")
	if err != nil {
		s.logger.Fatalf("failed to start listener on %s: %v", s.config.BindAddr, err)
		return
	}

	defer listener.Close()

	s.logger.Infof("server started successfully, listening on address: %s", listener.Addr().String())

	go s.acceptTunnelConn(listener)

	<-s.ctx.Done()
}

func (s *TcpUMuxTransport) acceptTunnelConn(listener net.Listener) {
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
			s.logger.Debugf("waiting for accept incoming tunnel connection on %s", listener.Addr().String())
			conn, err := listener.Accept()
			if err != nil {
				s.logger.Debugf("failed to accept tunnel connection on %s: %v", listener.Addr().String(), err)
				continue
			}

			// try to establish a new channel
			if s.controlChannel == nil {
				s.logger.Info("control channel not found, attempting to establish a new session")
				select {
				case s.handshakeChannel <- conn: // ok
				default:
					s.logger.Warnf("control channel handshake in progress...")
					conn.Close()
				}
				continue
			}

			session, err := smux.Client(conn, s.smuxConfig)
			if err != nil {
				s.logger.Errorf("failed to create MUX session for connection %s: %v", conn.RemoteAddr().String(), err)
				conn.Close()
				continue
			}

			select {
			case s.tunnelChannel <- session: // ok
				s.logger.Debugf("incoming session put into tunnel channel")
			default:
				s.logger.Warnf("tunnel listener channel is full, discarding TCP connection from %s", conn.LocalAddr().String())
				session.Close()
			}
		}
	}

}

func (s *TcpUMuxTransport) ParseMatchers() {
	//we only support simple single port for now
	//443=helloworld,othermatcher,othermatcher2

	for _, stringsMatchers := range s.config.Matchers {
		parts := strings.Split(stringsMatchers, "=")

		var port, matchers string
		port = strings.TrimSpace(parts[0])
		portint, _ := strconv.Atoi(port)
		matchers = strings.TrimSpace(parts[1])

		s.PortAndMatchers[portint] = strings.Split(matchers, ",")
	}
}

func (s *TcpUMuxTransport) parsePortMappings() {
	for _, portMapping := range s.config.Ports {
		parts := strings.Split(portMapping, "=")

		var localAddr, remoteAddr string

		// Check if only a single port or a port range is provided (no "=" present)
		if len(parts) == 1 {
			localPortOrRange := strings.TrimSpace(parts[0])
			remoteAddr = localPortOrRange // If no remote addr is provided, use the local port as the remote port

			// Check if it's a port range
			if strings.Contains(localPortOrRange, "-") {
				rangeParts := strings.Split(localPortOrRange, "-")
				if len(rangeParts) != 2 {
					s.logger.Fatalf("invalid port range format: %s", localPortOrRange)
				}

				// Parse and validate start and end ports
				startPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
				if err != nil || startPort < 1 || startPort > 65535 {
					s.logger.Fatalf("invalid start port in range: %s", rangeParts[0])
				}

				endPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
				if err != nil || endPort < 1 || endPort > 65535 || endPort < startPort {
					s.logger.Fatalf("invalid end port in range: %s", rangeParts[1])
				}

				// Create listeners for all ports in the range
				for port := startPort; port <= endPort; port++ {
					localAddr = fmt.Sprintf(":%d", port)
					go s.localListener(localAddr, strconv.Itoa(port)) // Use port as the remoteAddr
					time.Sleep(1 * time.Millisecond)                  // for wide port ranges
				}
				continue
			} else {
				// Handle single port case
				port, err := strconv.Atoi(localPortOrRange)
				if err != nil || port < 1 || port > 65535 {
					s.logger.Fatalf("invalid port format: %s", localPortOrRange)
				}
				localAddr = fmt.Sprintf(":%d", port)
			}
		} else if len(parts) == 2 {
			// Handle "local=remote" format
			localPortOrRange := strings.TrimSpace(parts[0])
			remoteAddr = strings.TrimSpace(parts[1])

			// Check if local port is a range
			if strings.Contains(localPortOrRange, "-") {
				rangeParts := strings.Split(localPortOrRange, "-")
				if len(rangeParts) != 2 {
					s.logger.Fatalf("invalid port range format: %s", localPortOrRange)
				}

				// Parse and validate start and end ports
				startPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
				if err != nil || startPort < 1 || startPort > 65535 {
					s.logger.Fatalf("invalid start port in range: %s", rangeParts[0])
				}

				endPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
				if err != nil || endPort < 1 || endPort > 65535 || endPort < startPort {
					s.logger.Fatalf("invalid end port in range: %s", rangeParts[1])
				}

				// Create listeners for all ports in the range
				for port := startPort; port <= endPort; port++ {
					localAddr = fmt.Sprintf(":%d", port)
					go s.localListener(localAddr, remoteAddr)
					time.Sleep(1 * time.Millisecond) // for wide port ranges
				}
				continue
			} else {
				// Handle single local port case
				port, err := strconv.Atoi(localPortOrRange)
				if err == nil && port > 1 && port < 65535 { // format port=remoteAddress
					localAddr = fmt.Sprintf(":%d", port)
				} else {
					localAddr = localPortOrRange // format ip:port=remoteAddress
				}
			}
		} else {
			s.logger.Fatalf("invalid port mapping format: %s", portMapping)
		}
		// Start listeners for single port
		go s.localListener(localAddr, remoteAddr)
	}
}

func (s *TcpUMuxTransport) localListener(localAddr string, remoteAddr string) {
	//listener, err := net.Listen("tcp", localAddr)
	listener, err := listenWithBuffers("tcp", localAddr, 1024*1024, 5*1024*1024, 1320, "bbr")
	if err != nil {
		s.logger.Fatalf("failed to start listener on %s: %v", localAddr, err)
		return
	}

	defer func() {
		s.logger.Warnf("LOCAL LISTENER GOT CLOSED!                -><<>>>-<>-")
		listener.Close()

	}()

	s.logger.Infof("listener started successfully, listening on address: %s", listener.Addr().String())

	go s.acceptLocalConn(listener, remoteAddr)

	<-s.ctx.Done()
}

// ExtractIP extracts the IP address from a TCP connection's remote address.
func ExtractIP(conn net.Conn) (string, error) {
	if conn == nil {
		return "", fmt.Errorf("connection is nil")
	}

	// Get the remote address as a string
	remoteAddr := conn.RemoteAddr().String()

	// Split the address into IP and port
	ip, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return "", fmt.Errorf("failed to parse remote address: %v", err)
	}

	return ip, nil
}

func (s *TcpUMuxTransport) acceptLocalConn(listener net.Listener, remoteAddr string) {

	defer func() {
		s.logger.Warnf("It's important to say acceptLocalConn is Closed")
	}()

	listenerPort, err := getPort(listener.Addr().String())
	if err != nil {
		s.logger.Errorf("error listener because we cannot extract the listener port %s", err)
		return
	}

	var matchersListForThisPort []string
	var matchers_exists bool
	var exists bool

	if matchersListForThisPort, exists = s.PortAndMatchers[listenerPort]; exists {
		matchers_exists = true
	} else {
		matchers_exists = false
	}

	var conn net.Conn

	for {
		select {
		case <-s.ctx.Done():
			s.logger.Warningf("S CTX IS DONE-> CLOSING ACCEPT LOCAL CHAN")
			return

		default:
			s.logger.Debugf("Listener is waiting to accept connection")
			conn, err = listener.Accept()
			s.logger.Debugf("Listener Accepted The Connection")
			if err != nil {
				s.logger.Debugf("failed to accept connection on %s: %v", listener.Addr().String(), err)
				continue
			}

			go AcceptLocalConnProcess(conn, s, matchers_exists, matchersListForThisPort, remoteAddr)

		}
	}

}

func AcceptLocalConnProcess(conn net.Conn, s *TcpUMuxTransport, matchers_exists bool, matchersListForThisPort []string, remoteAddr string) {
	//we are using this to prevent incoming connection from being blocked for processing that we may have

	var ThisIPuserTracker *UserTracker
	var err error
	var bfconn *BufferedConn
	var exists bool

	if matchers_exists {

		bfconn, err = NewBufferedConn(conn)
		if err != nil {
			s.logger.Warnf("error wrapping incoming conn into buffered connection %s CLOSING CONNECTION", err.Error())
			conn.Close()
			return
			//continue
		}

		connString := string(*bfconn.buffer)
		if !containsAny(connString, matchersListForThisPort) {
			s.logger.Debugf("connection coming from %s does not contain matchers we need", conn.RemoteAddr().String())
			conn.Close()
			return
			//continue
		}

		conn = bfconn
	}

	user_ip, err := ExtractIP(conn)
	if err != nil {
		s.logger.Debugf("error extracting user ip %s", err.Error())
		conn.Close()
		return
		//continue
	}

	s.logger.Debug("user ip is:", user_ip)

	s.UsersMapMutex.Lock()
	ThisIPuserTracker, exists = s.UsersMap[user_ip]
	s.UsersMapMutex.Unlock()
	if !exists {
		s.logger.Debugf("no UsersMap Exists creating new one")
		// Initialize a new User struct
		ThisIPuserTracker = NewUserTracker(user_ip)
		s.UsersMapMutex.Lock()
		s.UsersMap[user_ip] = ThisIPuserTracker
		s.UsersMapMutex.Unlock()
		s.logger.Debugf("we created new usermap")
	}

	if ThisIPuserTracker.userSession == nil || ThisIPuserTracker.userSession.IsClosed() {
		s.logger.Debug("userSession does not exists or it's closed, we should put new one")
		s.logger.Debugf("requesting new connection- >>>>>>>>>>>>>>>>>>>> DID WE LOCKED IN HERE?")
		s.RequestNewConnection()
		s.logger.Debugf("finished requesting for new connection")
	}

	if (ThisIPuserTracker.userSession == nil) || (ThisIPuserTracker.userSession.IsClosed()) {
		select {
		case <-s.ctx.Done():
			s.logger.Debugf("every thing is closed, s.ctx is done")
			conn.Close()
			return
		case ThisIPuserTracker.userSession = <-s.tunnelChannel:
			//this seems blocking if there is no tunnel channel available
			s.logger.Debugf("Nice, this user ip", ThisIPuserTracker.IP, "got a tunnel channel for itself")
		case <-time.After(time.Second * 3):
			s.logger.Warn("after 3 seconds we could not get a user session for user", conn.RemoteAddr().String(), "so dropping it's connection")
			conn.Close()
			return
		}
	}

	select {
	case ThisIPuserTracker.userLocalChannel <- LocalTCPConn{conn: conn, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()}:
		//s.localChannel <- LocalTCPConn{conn: conn, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()}:
		s.logger.Debugf("accepted incoming TCP connection from %s", conn.RemoteAddr().String())

		//انتظار میره که بر اساس لوکال کان استریم های جدیدی ایجاد بشه و کپی بین یوزر و سرور اتفاق بیفته
		ThisIPuserTracker.onceHandleUser.Do(func() {
			go func() {
				ThisIPuserTracker.handleUserSession(s)
			}()
		})

		ThisIPuserTracker.onceTrackSession.Do(func() {
			go func() {
				ThisIPuserTracker.TrackSessionStreams(s)
			}()
		})

	default: // channel is full, discard the connection
		s.logger.Warnf("local listener channel is full, discarding TCP connection from %s", conn.LocalAddr().String())
		conn.Close()
	}

}

func (s *TcpUMuxTransport) RequestNewConnection() {
	//this is blocking until we can create new connection
	//for {

	if len(s.tunnelChannel) > 20 {
		s.logger.Warn("becaus we had more than 20 ready connections in tunnel channel we did not request new ones")
		return
	}

	select {
	case s.reqNewConnChan <- struct{}{}:
		s.logger.Warn("we requested new channel")
		return
	default:
		s.logger.Warn("failed to request new connection. channel is full")
		//time.Sleep(time.Duration(1 * time.Second))
	}
	//}
}

func (ut *UserTracker) fillTheSessionIfClosed(s *TcpUMuxTransport) {
	if ut.userSession.IsClosed() {
		//اگر بسته بود یکی جدید میسازیم
		go s.RequestNewConnection()

		for {
			select {
			case new_session_candidate := <-s.tunnelChannel:
				if new_session_candidate.IsClosed() {
					s.logger.Warn("session that was lying inide tunnel channel was closed, retry another one")
					continue
				}

				ut.userSession = new_session_candidate
				return

			case <-time.After(time.Second * 3):
				s.logger.Warn("user session does not got filled after 3 seconds!")
				return
			}
		}

	}

}

func (ut *UserTracker) handleUserSession(s *TcpUMuxTransport) {
	s.logger.Debugf("hanle user session comes up")

	defer s.logger.Debugf("handle user session comes down")

	for {
		select {
		case <-ut.ctx.Done():
			//ut.userSession.Close()
			return

		case incomingConn, ok := <-ut.userLocalChannel:
			if !ok {
				s.logger.Debugf("Session gets closed because userLocalChannel gets Closed")
				return
			}

			s.logger.Debugf("new user local channel coming that we want to work on")

			if time.Now().UnixMilli()-incomingConn.timeCreated > 3000 { // 3000ms
				s.logger.Debugf("timeouted local connection: %d ms", time.Now().UnixMilli()-incomingConn.timeCreated)
				incomingConn.conn.Close()
				continue
			}

			if ut.userSession == nil {

				select {
				case ut.userLocalChannel <- incomingConn:
				default:
					s.logger.Debug("user local channel was full, so we had to close the incoming conn")
					incomingConn.conn.Close()
				}

				ut.fillTheSessionIfClosed(s)
				continue
			}

			//t1 := time.Now()
			stream, err := ut.userSession.OpenStream()
			if err != nil {
				s.logger.Errorf("failed to open stream: %v", err)

				select {
				case ut.userLocalChannel <- incomingConn:
				default:
					s.logger.Debug("user local channel was full, so we had to close the incoming conn")
					incomingConn.conn.Close()
				}

				ut.fillTheSessionIfClosed(s)

				//اینجا اگر نتونه استریم رو باز کنه حدس میزنیم که سشن قطع شده دوباره یکی دیگه میسازیم جایگزین قبلی میکنیم
				//و قبلی رو کلوز میکنیم

				continue
			}

			//t2 := time.Now()

			//s.logger.Debug("an stream is created in", t2.Sub(t1).Milliseconds(), "miliseconds")

			// Send the target port over the tunnel connection
			if err := utils.SendBinaryString(stream, incomingConn.remoteAddr); err != nil {
				s.logger.Errorf("failed to handle session: %v", err)
				//ut.handleUserSessionError(&incomingConn)

				select {
				case ut.userLocalChannel <- incomingConn:
				default:
					s.logger.Debug("user local channel was full, so we had to close the incoming conn")
					incomingConn.conn.Close()
				}
				stream.Close()

				ut.fillTheSessionIfClosed(s)

				continue
			}

			// Handle data exchange between connections
			go func() {
				utils.TCPConnectionHandler(stream, incomingConn.conn, s.logger, s.usageMonitor, incomingConn.conn.LocalAddr().(*net.TCPAddr).Port, s.config.Sniffer)
			}()
		}
	}
}
