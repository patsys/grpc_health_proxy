// Copyright 2018 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io/ioutil"
        "gopkg.in/yaml.v2"
	"os"
	"github.com/golang/glog"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/gorilla/mux"
	"golang.org/x/net/http2"

)

type Config struct {
	FlServer ServerConfig `yaml:"server,omitempty"`
	FlProbeConfig ProbeConfig `yaml:"probe,omitempty"`
	FlProbeRequestConifg map[string]ProbeRequestConfig `yaml:"hosts"`
}

type ServerConfig struct {
	FlRunCli         bool `yaml:"cli,omitempty"`
	FlServiceName    string `yaml:"service,omitempty"`
	FlHTTPListenAddr string `yaml:"address,omitempty"`
	FlHTTPListenPath string `yaml:"path,omitempty"`
	FlHTTPSTLSServerCert string `yaml:"certPath,omitempty"`
	FlHTTPSTLSServerKey string `yaml:"keyPath,omitempty"`
	FlHTTPSTLSVerifyCA string `yaml:"mtlsCA,omitempty"`
	FlHTTPSTLSVerifyClient bool `yaml:"mtlsEnabled,omitempty"`
	FlConnTimeout   time.Duration `yaml:"timeout,omitempty"`
}
type ProbeConfig struct {
	FlUserAgent     string `yaml:"userAgent,omitempty"`
	FlRPCTimeout    time.Duration `yaml:"timeout,omitempty"`
}

type ProbeRequestConfig struct {
	FlGrpcServerAddr string `yaml:"address,omitempty"`
	FlGrpcTLS       bool `yaml:"tlsEnabled,omitempty"`
	FlGrpcTLSNoVerify   bool `yaml:"tlsNoVerfiy,omitempty"`
	FlGrpcTLSCACert     string `yaml:"tlsCaPath,omitempty"`
	FlGrpcTLSClientCert string `yaml:"mtlsCertPath,omitempty"`
	FlGrpcTLSClientKey  string `yaml:"mtlsKeyPath,omitempty"`
	FlGrpcSNIServerName string `yaml:"sniServerName,omitempty"`
	opts []grpc.DialOption
}

var (
	cfg = &Config{}
	configFlag string
)

type GrpcProbeError struct {
	Code int
	Message string
}

func NewGrpcProbeError(code int, message string) *GrpcProbeError {
    return &GrpcProbeError{
		Code: code,
		Message: message,
	}
}
func (e *GrpcProbeError) Error() string {
    return e.Message
}
const (
	StatusConnectionFailure = 1
	StatusRPCFailure = 2
	StatusServiceNotFound = 3
	StatusUnimplemented = 4
	StatusUnhealthy = 5
)

func init() {
	var probeRequestConifg  = ProbeRequestConfig{}
	cfg.FlServer = ServerConfig{}
	cfg.FlProbeConfig = ProbeConfig{}
	cfg.FlProbeRequestConifg = make(map[string]ProbeRequestConfig)
	flag.StringVar(&configFlag, "config", "", "configFile")
	flag.StringVar(&cfg.FlProbeConfig.FlUserAgent, "user-agent", "grpc_health_proxy", "user-agent header value of health check requests")
	flag.BoolVar(&cfg.FlServer.FlRunCli, "runcli", false, "execute healthCheck via CLI; will not start webserver")
	flag.StringVar(&cfg.FlServer.FlServiceName, "service-name", "", "service name to check.  Used cli mode only")
	// settings for HTTPS lisenter
	flag.StringVar(&cfg.FlServer.FlHTTPListenAddr, "http-listen-addr", "localhost:8080", "(required) http host:port to listen (default: localhost:8080")
	flag.StringVar(&cfg.FlServer.FlHTTPListenPath, "http-listen-path", "/", "path to listen for healthcheck traffic (default '/')")
	flag.StringVar(&cfg.FlServer.FlHTTPSTLSServerCert, "https-listen-cert", "", "TLS Server certificate to for HTTP listner")
	flag.StringVar(&cfg.FlServer.FlHTTPSTLSServerKey, "https-listen-key", "", "TLS Server certificate key to for HTTP listner")
	flag.StringVar(&cfg.FlServer.FlHTTPSTLSVerifyCA, "https-listen-ca", "", "Use CA to verify client requests against CA")
	flag.BoolVar(&cfg.FlServer.FlHTTPSTLSVerifyClient, "https-listen-verify", false, "Verify client certificate provided to the HTTP listner")
	// timeouts
	flag.DurationVar(&cfg.FlServer.FlConnTimeout, "connect-timeout", time.Second, "timeout for establishing connection")
	flag.DurationVar(&cfg.FlProbeConfig.FlRPCTimeout, "rpc-timeout", time.Second, "timeout for health check rpc")
	// tls settings
	flag.BoolVar(&probeRequestConifg.FlGrpcTLS, "grpctls", false, "use TLS for upstream gRPC(default: false, INSECURE plaintext transport)")
	flag.BoolVar(&probeRequestConifg.FlGrpcTLSNoVerify, "grpc-tls-no-verify", false, "(with -tls) don't verify the certificate (INSECURE) presented by the server (default: false)")
	flag.StringVar(&probeRequestConifg.FlGrpcTLSCACert, "grpc-ca-cert", "", "(with -tls, optional) file containing trusted certificates for verifying server")
	flag.StringVar(&probeRequestConifg.FlGrpcTLSClientCert, "grpc-client-cert", "", "(with -grpctls, optional) client certificate for authenticating to the server (requires -tls-client-key)")
	flag.StringVar(&probeRequestConifg.FlGrpcTLSClientKey, "grpc-client-key", "", "(with -grpctls) client private key for authenticating to the server (requires -tls-client-cert)")
	flag.StringVar(&probeRequestConifg.FlGrpcSNIServerName, "grpc-sni-server-name", "", "(with -grpctls) override the hostname used to verify the gRPC server certificate")
	flag.StringVar(&probeRequestConifg.FlGrpcServerAddr, "grpcaddr", "", "(required) tcp host:port to connect")

	flag.Parse()

	cfg.FlProbeRequestConifg["cli"] = probeRequestConifg

	if configFlag != "" {
		yamlFile, err := ioutil.ReadFile(configFlag)
		if err != nil {
			glog.Fatalf("Cannot get config file %s Get err   #%v ", configFlag, err)
			os.Exit(-1)
		}
		err = yaml.Unmarshal(yamlFile, &cfg)
		if err != nil {
			glog.Fatalf("Config parse error: %v", err)
			os.Exit(-1)
		}
	}
	argError := func(s string, v ...interface{}) {
		//Flag.PrintDefaults()
		glog.Errorf("Invalid Argument error: "+s, v...)
		os.Exit(-1)
	}
	if !cfg.FlServer.FlRunCli && cfg.FlServer.FlHTTPListenAddr == "" {
		argError("-http-listen-addr not specified or server.address not set")
	}
	if cfg.FlServer.FlConnTimeout <= 0 {
		argError("-connect-timeout or server.timeout must be greater than zero (specified: %v)", cfg.FlServer.FlConnTimeout)
	}
	if cfg.FlProbeConfig.FlRPCTimeout <= 0 {
		argError("-rpc-timeout or probe.config must be greater than zero (specified: %v)", cfg.FlProbeConfig.FlRPCTimeout)
	}
	if ( (cfg.FlServer.FlHTTPSTLSServerCert == "" && cfg.FlServer.FlHTTPSTLSServerKey != "") || (cfg.FlServer.FlHTTPSTLSServerCert != "" && cfg.FlServer.FlHTTPSTLSServerKey == "") ) {
		argError("must specify both -https-listen-cert and -https-listen-key")
	}
	if cfg.FlServer.FlHTTPSTLSVerifyCA == "" && cfg.FlServer.FlHTTPSTLSVerifyClient {
		argError("cannot specify -https-listen-ca if https-listen-verify is set (you need a trust CA for client certificate https auth)")
	}
	for path, config := range cfg.FlProbeRequestConifg {
		if config.FlGrpcServerAddr == "" {
			argError("-grpcaddr(hosts."+ path + ".address) not specified")
		}
		if !config.FlGrpcTLS && config.FlGrpcTLSNoVerify {
			argError("specified -grpc-tls-no-verify(hosts."+ path + ".tlsNoVerfiy) without specifying -grpctls(hosts."+ path + ".tlsEnabled)")
		}
		if !config.FlGrpcTLS && config.FlGrpcTLSCACert != "" {
			argError("specified -grpc-ca-cert(hosts."+ path + ".tlsCaPath) without specifying -grpctls(hosts."+ path + ".tlsEnabled")
		}
		if !config.FlGrpcTLS && config.FlGrpcTLSClientCert != "" {
			argError("specified -grpc-client-cert(hosts."+ path + ".mtlsCertPath) without specifying -grpctls(hosts."+ path + ".tlsEnabled)")
		}
		if !config.FlGrpcTLS && config.FlGrpcSNIServerName != "" {
			argError("specified -grpc-sni-server-name(hosts."+ path + ".sniServerName) without specifying -grpctls(hosts."+ path + ".tlsEnabled)")
		}
		if config.FlGrpcTLSClientCert != "" && config.FlGrpcTLSClientKey == "" {
			argError("specified -grpc-client-cert(hosts."+ path + ".mtlsCertPath) without specifying -grpc-client-key(hosts."+ path + ".mtlsKeyPath)")
		}
		if config.FlGrpcTLSClientCert == "" && config.FlGrpcTLSClientKey != "" {
			argError("specified -grpc-client-key(hosts."+ path + ".mtlsKeyPath) without specifying -grpc-client-cert(hosts."+ path + ".mtlsCertPath")
		}
		if config.FlGrpcTLSNoVerify && config.FlGrpcTLSCACert != "" {
			argError("cannot specify -grpc-ca-cert(hosts."+ path + ".tlsCaPath) with -grpc-tls-no-verify(hosts."+ path + ".tlsNoVerify) (CA cert would not be used)")
		}
		if config.FlGrpcTLSNoVerify && config.FlGrpcSNIServerName != "" {
			argError("cannot specify -grpc-sni-server-name(hosts."+ path + ".sniServerName) with -grpc-tls-no-verify(hosts."+ path + ".tlsNoVerify) (server name would not be used)")
		}
	}

	glog.V(10).Infof("parsed options:")
	glog.V(10).Infof("> conn_timeout=%s rpc_timeout=%s",cfg.FlServer.FlConnTimeout, cfg.FlProbeConfig.FlRPCTimeout)
	glog.V(10).Infof(" http-listen-addr=%s ", cfg.FlServer.FlHTTPListenAddr)
	glog.V(10).Infof(" http-listen-path=%s ", cfg.FlServer.FlHTTPListenPath)
	glog.V(10).Infof(" https-listen-cert=%s ", cfg.FlServer.FlHTTPSTLSServerCert)
	glog.V(10).Infof(" https-listen-key=%s ", cfg.FlServer.FlHTTPSTLSServerKey)
	glog.V(10).Infof(" https-listen-verify=%v ", cfg.FlServer.FlHTTPSTLSVerifyClient)
	glog.V(10).Infof(" https-listen-ca=%s ", cfg.FlServer.FlHTTPSTLSVerifyCA)


	for path, config := range cfg.FlProbeRequestConifg {
		glog.V(10).Infof(">(" + path + ") grpctls=%v", config.FlGrpcTLS)
		glog.V(10).Infof("  > grpc-tls-no-verify=%v ", config.FlGrpcTLSNoVerify)
		glog.V(10).Infof("  > grpc-ca-cert=%s", config.FlGrpcTLSCACert)
		glog.V(10).Infof("  > grpc-client-cert=%s", config.FlGrpcTLSClientCert)
		glog.V(10).Infof("  > grpc-client-key=%s", config.FlGrpcTLSClientKey)
		glog.V(10).Infof("  > grpc-sni-server-name=%s", config.FlGrpcSNIServerName)
	}
}

func buildGrpcCredentials(hostConfig ProbeRequestConfig) (credentials.TransportCredentials, error) {
	var tlsCfg tls.Config

	if hostConfig.FlGrpcTLSClientCert != "" && hostConfig.FlGrpcTLSClientKey != "" {
		keyPair, err := tls.LoadX509KeyPair(hostConfig.FlGrpcTLSClientCert, hostConfig.FlGrpcTLSClientKey)
		if err != nil {
			return nil, fmt.Errorf("failed to load tls client cert/key pair. error=%v", err)
		}
		tlsCfg.Certificates = []tls.Certificate{keyPair}
	}

	if hostConfig.FlGrpcTLSNoVerify {
		tlsCfg.InsecureSkipVerify = true
	} else if hostConfig.FlGrpcTLSCACert != "" {
		rootCAs := x509.NewCertPool()
		pem, err := ioutil.ReadFile(hostConfig.FlGrpcTLSCACert)
		if err != nil {
			return nil, fmt.Errorf("failed to load root CA certificates from file (%s) error=%v", hostConfig.FlGrpcTLSCACert, err)
		}
		if !rootCAs.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no root CA certs parsed from file %s", hostConfig.FlGrpcTLSCACert)
		}
		tlsCfg.RootCAs = rootCAs
	}
	if hostConfig.FlGrpcSNIServerName != "" {
		tlsCfg.ServerName = hostConfig.FlGrpcSNIServerName
	}
	return credentials.NewTLS(&tlsCfg), nil
}

func checkService(ctx context.Context, hostConfig ProbeRequestConfig, serviceName string) (healthpb.HealthCheckResponse_ServingStatus, error) {

	glog.V(10).Infof("establishing connection")
	connStart := time.Now()
	dialCtx, dialCancel := context.WithTimeout(ctx, cfg.FlServer.FlConnTimeout)
	defer dialCancel()
	conn, err := grpc.DialContext(dialCtx, hostConfig.FlGrpcServerAddr, hostConfig.opts...)
	if err != nil {
		if err == context.DeadlineExceeded {
			glog.Warningf("timeout: failed to connect %s service %s within %s", hostConfig.FlGrpcServerAddr, cfg.FlServer.FlConnTimeout)
		} else {
			glog.Warningf("error: failed to connect service at %s: %+v", hostConfig.FlGrpcServerAddr, err)
		}
		return healthpb.HealthCheckResponse_UNKNOWN, NewGrpcProbeError(StatusConnectionFailure, "StatusConnectionFailure")
	}
	connDuration := time.Since(connStart)
	defer conn.Close()
	glog.V(10).Infof("connection established %v", connDuration)

	rpcStart := time.Now()
	rpcCtx, rpcCancel := context.WithTimeout(ctx, cfg.FlProbeConfig.FlRPCTimeout)
	defer rpcCancel()

	glog.V(10).Infoln("Running HealthCheck for service: ", serviceName)

	resp, err := healthpb.NewHealthClient(conn).Check(rpcCtx, &healthpb.HealthCheckRequest{Service: serviceName})
	if err != nil {
		// first handle and return gRPC-level errors
		if stat, ok := status.FromError(err); ok && stat.Code() == codes.Unimplemented {
			glog.Warningf("error: this server does not implement the grpc health protocol (grpc.health.v1.Health)")
			return healthpb.HealthCheckResponse_UNKNOWN, NewGrpcProbeError(StatusUnimplemented, "StatusUnimplemented")
		} else if stat, ok := status.FromError(err); ok && stat.Code() == codes.DeadlineExceeded {
			glog.Warningf("error timeout: health rpc did not complete within ", cfg.FlProbeConfig.FlRPCTimeout)
			return healthpb.HealthCheckResponse_UNKNOWN, NewGrpcProbeError(StatusRPCFailure, "StatusRPCFailure")
		} else if stat, ok := status.FromError(err); ok && stat.Code() == codes.NotFound {
			// wrap a grpC NOT_FOUND as grpcProbeError.
			// https://github.com/grpc/grpc/blob/master/doc/health-checking.md
			// if the service name is not registerered, the server returns a NOT_FOUND GPRPC status. 
			// the Check for a not found should "return nil, status.Error(codes.NotFound, "unknown service")"
			glog.Warningf("error Service Not Found %v", err )
			return healthpb.HealthCheckResponse_SERVICE_UNKNOWN, NewGrpcProbeError(StatusServiceNotFound, "StatusServiceNotFound")
		} else {
			glog.Warningf("error: health rpc failed: ", err)
		}
	}
	rpcDuration := time.Since(rpcStart)
	// otherwise, retrurn gRPC-HC status
	glog.V(10).Infof("time elapsed: connect=%s rpc=%s", connDuration, rpcDuration)

	return resp.GetStatus(), nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var host string
	var serviceName string
	var probeRequestConfig ProbeRequestConfig

	if value, exist := vars["host"]; exist {
		host = value
		if config, cExist := cfg.FlProbeRequestConifg[value]; cExist {
			probeRequestConfig = config
		} else {
			http.Error(w, fmt.Sprintf("%s HostNotFound", host), http.StatusNotFound)
			return
		}
	} else {
		return // healtcheck only webserver start
	}

	if value, exist := vars["service"]; exist {
		serviceName = value
	} else {
		serviceName = "" // healtcheck grpc exist on host
	}

	resp, err := checkService(r.Context(), probeRequestConfig, serviceName)
	// first handle errors derived from gRPC-codes
	if err != nil {
        if pe, ok := err.(*GrpcProbeError); ok {
			glog.Errorf("HealtCheck Probe Error: %v", pe.Error())
			switch pe.Code {
			case StatusConnectionFailure:
				http.Error(w, err.Error(), http.StatusBadGateway)
			case StatusRPCFailure:
				http.Error(w, err.Error(), http.StatusBadGateway)
			case StatusUnimplemented:
				http.Error(w, err.Error(), http.StatusNotImplemented)
			case StatusServiceNotFound:
				http.Error(w, fmt.Sprintf("%s ServiceNotFound", serviceName), http.StatusNotFound)
			default:
				http.Error(w, err.Error(), http.StatusBadGateway)
			}
			return
		}
	}

	// then grpc-hc codes
	glog.Infof("%s %v",  serviceName,  resp.String())
	switch resp {
	case healthpb.HealthCheckResponse_SERVING:
		fmt.Fprintf(w, "%s %v", serviceName, resp)
	case healthpb.HealthCheckResponse_NOT_SERVING:
		http.Error(w, fmt.Sprintf("%s %v", serviceName, resp.String()), http.StatusBadGateway)
	case healthpb.HealthCheckResponse_UNKNOWN:
		http.Error(w, fmt.Sprintf("%s %v", serviceName, resp.String()), http.StatusBadGateway)
	case healthpb.HealthCheckResponse_SERVICE_UNKNOWN:
		http.Error(w, fmt.Sprintf("%s %v", serviceName, resp.String()), http.StatusNotFound)
	}
}

func main() {


	for key, config := range cfg.FlProbeRequestConifg {
		config.opts = []grpc.DialOption{}
		config.opts = append(config.opts, grpc.WithUserAgent(cfg.FlProbeConfig.FlUserAgent))
		config.opts = append(config.opts, grpc.WithBlock())
		if config.FlGrpcTLS {
			creds, err := buildGrpcCredentials(config)
			if err != nil {
				glog.Fatalf("failed to initialize tls credentials. error=%v", err)
			}
			config.opts = append(config.opts, grpc.WithTransportCredentials(creds))
		} else {
			config.opts = append(config.opts, grpc.WithInsecure())
		}
		cfg.FlProbeRequestConifg[key] = config
	}

	if (cfg.FlServer.FlRunCli) {
		resp, err := checkService(context.Background(), cfg.FlProbeRequestConifg["cli"], cfg.FlServer.FlServiceName)
		if err != nil {
			if pe, ok := err.(*GrpcProbeError); ok {
				glog.Errorf("HealtCheck Probe Error: %v", pe.Error())
				switch pe.Code {
				case StatusConnectionFailure:
					os.Exit(StatusConnectionFailure)
				case StatusRPCFailure:
					os.Exit(StatusRPCFailure)
				case StatusUnimplemented:
					os.Exit(StatusUnimplemented)
				case StatusServiceNotFound:
					os.Exit(StatusServiceNotFound)
				default:
					os.Exit(StatusUnhealthy)
				}
			}
		}
		if (resp != healthpb.HealthCheckResponse_SERVING) {
			glog.Errorf("HealtCheck Probe Error: service %s failed with reason: %v",  cfg.FlServer.FlServiceName,  resp.String())
			os.Exit(StatusUnhealthy)
		} else {
			glog.Infof("%s %v",  cfg.FlServer.FlServiceName,  resp.String())
		}
	} else {

		tlsConfig := &tls.Config{}
		if (cfg.FlServer.FlHTTPSTLSVerifyClient) {
			caCert, err := ioutil.ReadFile(cfg.FlServer.FlHTTPSTLSVerifyCA)
			if err != nil {
				glog.Fatal(err)
			}
			caCertPool := x509.NewCertPool()
			if !caCertPool.AppendCertsFromPEM(caCert){
				glog.Fatal("Unable to add https server root CA certs")
			}

			tlsConfig = &tls.Config{
				ClientCAs: caCertPool,
				ClientAuth: tls.RequireAndVerifyClientCert,
			}
		}
		tlsConfig.BuildNameToCertificate()

		srv := &http.Server{
			Addr: cfg.FlServer.FlHTTPListenAddr,
			TLSConfig: tlsConfig,
		}
		http2.ConfigureServer(srv, &http2.Server{})
		rtr := mux.NewRouter()
		rtr.HandleFunc(cfg.FlServer.FlHTTPListenPath + "/{host:.+}/{service:.+}", healthHandler)
                rtr.HandleFunc(cfg.FlServer.FlHTTPListenPath + "/{host:.+}", healthHandler)
                rtr.HandleFunc(cfg.FlServer.FlHTTPListenPath + "/", healthHandler)
		http.Handle("/", rtr)

		var err error
		if (cfg.FlServer.FlHTTPSTLSServerCert != "" && cfg.FlServer.FlHTTPSTLSServerKey != "" ) {
			err = srv.ListenAndServeTLS(cfg.FlServer.FlHTTPSTLSServerCert, cfg.FlServer.FlHTTPSTLSServerKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil {
			glog.Fatalf("ListenAndServe Error: ", err)
		}
	}
}
