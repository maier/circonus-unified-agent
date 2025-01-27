package mongodb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/circonus-labs/circonus-unified-agent/cua"
	tlsint "github.com/circonus-labs/circonus-unified-agent/plugins/common/tls"
	"github.com/circonus-labs/circonus-unified-agent/plugins/inputs"
	"gopkg.in/mgo.v2"
)

type MongoDB struct {
	Servers             []string
	Ssl                 Ssl
	mongos              map[string]*Server
	GatherClusterStatus bool
	GatherPerdbStats    bool
	GatherColStats      bool
	ColStatsDbs         []string
	tlsint.ClientConfig

	Log cua.Logger
}

type Ssl struct {
	Enabled bool
	CaCerts []string `toml:"cacerts"`
}

var sampleConfig = `
  instance_id = "" # unique instance identifier (REQUIRED)

  ## An array of URLs of the form:
  ##   "mongodb://" [user ":" pass "@"] host [ ":" port]
  ## For example:
  ##   mongodb://user:auth_key@10.10.3.30:27017,
  ##   mongodb://10.10.3.33:18832,
  servers = ["mongodb://127.0.0.1:27017"]

  ## When true, collect cluster status
  ## Note that the query that counts jumbo chunks triggers a COLLSCAN, which
  ## may have an impact on performance.
  # gather_cluster_status = true

  ## When true, collect per database stats
  # gather_perdb_stats = false

  ## When true, collect per collection stats
  # gather_col_stats = false

  ## List of db where collections stats are collected
  ## If empty, all db are concerned
  # col_stats_dbs = ["local"]

  ## Optional TLS Config
  # tls_ca = "/etc/circonus-unified-agent/ca.pem"
  # tls_cert = "/etc/circonus-unified-agent/cert.pem"
  # tls_key = "/etc/circonus-unified-agent/key.pem"
  ## Use TLS but skip chain & host verification
  # insecure_skip_verify = false
`

func (m *MongoDB) SampleConfig() string {
	return sampleConfig
}

func (*MongoDB) Description() string {
	return "Read metrics from one or many MongoDB servers"
}

var localhost = &url.URL{Host: "mongodb://127.0.0.1:27017"}

// Reads stats from all configured servers accumulates stats.
// Returns one of the errors encountered while gather stats (if any).
func (m *MongoDB) Gather(ctx context.Context, acc cua.Accumulator) error {
	if len(m.Servers) == 0 {
		_ = m.gatherServer(m.getMongoServer(localhost), acc)
		return nil
	}

	var wg sync.WaitGroup
	for i, serv := range m.Servers {
		if !strings.HasPrefix(serv, "mongodb://") {
			// Preserve backwards compatibility for hostnames without a
			// scheme, broken in go 1.8. Remove in agent v2.0
			serv = "mongodb://" + serv
			m.Log.Warnf("Using %q as connection URL; please update your configuration to use an URL", serv)
			m.Servers[i] = serv
		}

		u, err := url.Parse(serv)
		if err != nil {
			m.Log.Errorf("Unable to parse address %q: %s", serv, err.Error())
			continue
		}
		if u.Host == "" {
			m.Log.Errorf("Unable to parse address %q", serv)
			continue
		}

		wg.Add(1)
		go func(srv *Server) {
			defer wg.Done()
			err := m.gatherServer(srv, acc)
			if err != nil {
				m.Log.Errorf("Error in plugin: %v", err)
			}
		}(m.getMongoServer(u))
	}

	wg.Wait()
	return nil
}

func (m *MongoDB) getMongoServer(url *url.URL) *Server {
	if _, ok := m.mongos[url.Host]; !ok {
		m.mongos[url.Host] = &Server{
			Log: m.Log,
			URL: url,
		}
	}
	return m.mongos[url.Host]
}

func (m *MongoDB) gatherServer(server *Server, acc cua.Accumulator) error {
	if server.Session == nil {
		var dialAddrs []string
		if server.URL.User != nil {
			dialAddrs = []string{server.URL.String()}
		} else {
			dialAddrs = []string{server.URL.Host}
		}
		dialInfo, err := mgo.ParseURL(dialAddrs[0])
		if err != nil {
			return fmt.Errorf("unable to parse URL %q: %w", dialAddrs[0], err)
		}
		dialInfo.Direct = true
		dialInfo.Timeout = 5 * time.Second

		var tlsConfig *tls.Config

		if m.Ssl.Enabled {
			// Deprecated TLS config
			tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			if len(m.Ssl.CaCerts) > 0 {
				roots := x509.NewCertPool()
				for _, caCert := range m.Ssl.CaCerts {
					ok := roots.AppendCertsFromPEM([]byte(caCert))
					if !ok {
						return fmt.Errorf("failed to parse root certificate")
					}
				}
				tlsConfig.RootCAs = roots
			} else {
				tlsConfig.InsecureSkipVerify = true
			}
		} else {
			tlsConfig, err = m.ClientConfig.TLSConfig()
			if err != nil {
				return fmt.Errorf("TLSConfig: %w", err)
			}
		}

		// If configured to use TLS, add a dial function
		if tlsConfig != nil {
			dialInfo.DialServer = func(addr *mgo.ServerAddr) (net.Conn, error) {
				conn, err := tls.Dial("tcp", addr.String(), tlsConfig)
				if err != nil {
					fmt.Printf("error in Dial, %s\n", err.Error())
				}
				return conn, fmt.Errorf("tls dial (%s): %w", addr.String(), err)
			}
		}

		sess, err := mgo.DialWithInfo(dialInfo)
		if err != nil {
			return fmt.Errorf("unable to connect to MongoDB: %w", err)
		}
		server.Session = sess
	}
	return server.gatherData(acc, m.GatherClusterStatus, m.GatherPerdbStats, m.GatherColStats, m.ColStatsDbs)
}

func init() {
	inputs.Add("mongodb", func() cua.Input {
		return &MongoDB{
			mongos:              make(map[string]*Server),
			GatherClusterStatus: true,
			GatherPerdbStats:    false,
			GatherColStats:      false,
			ColStatsDbs:         []string{"local"},
		}
	})
}
