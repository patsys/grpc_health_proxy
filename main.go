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
	flServer ServerConfig `yaml:"server"`
	flProbeConfig ProbeConfig `yaml:"probe"`
	flProbeRequestConifg map[string]ProbeRequestConfig `yaml:"hosts,omitemty"`
}

type ServerConfig struct {
	flRunCli         bool
	flServiceName    string
	flHTTPListenAddr string `yaml:"address"`
	flHTTPListenPath string `yaml:"path"`
	flHTTPSTLSServerCert string `yaml:"certPath"`
	flHTTPSTLSServerKey string `yaml:"keyPath"`
	flHTTPSTLSVerifyCA string `yaml:"mtlsCA"`
	flHTTPSTLSVerifyClient bool `yaml:"mtlsEnabled"`
	flConnTimeout   time.Duration `yaml:"timeout"`
}
type ProbeConfig struct {
	flUserAgent     string `yaml:"userAgent"`
	flRPCTimeout    time.Duration `yaml:"timeout"`
}

type ProbeRequestConfig struct {
	flGrpcServerAddr string `yaml:"address"`
	flGrpcTLS       bool `yaml:tlsEnabled"`
	flGrpcTLSNoVerify   bool `yaml:"tlsNoVerfiy"`
	flGrpcTLSCACert     string `yaml:"tlsCaPath"`
	flGrpcTLSClientCert string `yaml:"mtlsCertPath"`
	flGrpcTLSClientKey  string `yaml:"mtlsKeyPath"`
	flGrpcSNIServerName string `yaml:"sniServerName"`
	flHttpListenPath string `yaml:"path"`
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
	cfg.flServer = ServerConfig{}
	cfg.flProbeConfig = ProbeConfig{}
	cfg.flProbeRequestConifg = make(map[string]ProbeRequestConfig)
	flag.StringVar(&configFlag, "config", "", "configFile")
	flag.StringVar(&cfg.flProbeConfig.flUserAgent, "user-agent", "grpc_health_proxy", "user-agent header value of health check requests")
	flag.BoolVar(&cfg.flServer.flRunCli, "runcli", false, "execute healthCheck via CLI; will not start webserver")
	flag.StringVar(&cfg.flServer.flServiceName, "service-name", "", "service name to check.  If specified, server will ignore ?serviceName= request parameter")
	// settings for HTTPS lisenter
	flag.StringVar(&cfg.flServer.flHTTPListenAddr, "http-listen-addr", "localhost:8080", "(required) http host:port to listen (default: localhost:8080")
	flag.StringVar(&cfg.flServer.flHTTPListenPath, "http-listen-path", "/", "path to listen for healthcheck traffic (default '/')")
	flag.StringVar(&cfg.flServer.flHTTPSTLSServerCert, "https-listen-cert", "", "TLS Server certificate to for HTTP listner")
	flag.StringVar(&cfg.flServer.flHTTPSTLSServerKey, "https-listen-key", "", "TLS Server certificate key to for HTTP listner")
	flag.StringVar(&cfg.flServer.flHTTPSTLSVerifyCA, "https-listen-ca", "", "Use CA to verify client requests against CA")
	flag.BoolVar(&cfg.flServer.flHTTPSTLSVerifyClient, "https-listen-verify", false, "Verify client certificate provided to the HTTP listner")
	// timeouts
	flag.DurationVar(&cfg.flServer.flConnTimeout, "connect-timeout", time.Second, "timeout for establishing connection")
	flag.DurationVar(&cfg.flProbeConfig.flRPCTimeout, "rpc-timeout", time.Second, "timeout for health check rpc")
	// tls settings
	flag.BoolVar(&probeRequestConifg.flGrpcTLS, "grpctls", false, "use TLS for upstream gRPC(default: false, INSECURE plaintext transport)")
	flag.BoolVar(&probeRequestConifg.flGrpcTLSNoVerify, "grpc-tls-no-verify", false, "(with -tls) don't verify the certificate (INSECURE) presented by the server (default: false)")
	flag.StringVar(&probeRequestConifg.flGrpcTLSCACert, "grpc-ca-cert", "", "(with -tls, optional) file containing trusted certificates for verifying server")
	flag.StringVar(&probeRequestConifg.flGrpcTLSClientCert, "grpc-client-cert", "", "(with -grpctls, optional) client certificate for authenticating to the server (requires -tls-client-key)")
	flag.StringVar(&probeRequestConifg.flGrpcTLSClientKey, "grpc-client-key", "", "(with -grpctls) client private key for authenticating to the server (requires -tls-client-cert)")
	flag.StringVar(&probeRequestConifg.flGrpcSNIServerName, "grpc-sni-server-name", "", "(with -grpctls) override the hostname used to verify the gRPC server certificate")
	flag.StringVar(&probeRequestConifg.flGrpcServerAddr, "grpcaddr", "", "(required) tcp host:port to connect")

	flag.Parse()

	cfg.flProbeRequestConifg["cli"] = probeRequestConifg

	if configFlag != "" {
		yamlFile, err := ioutil.ReadFile("conf.yaml")
		if err != nil {
			glog.Fatalf("Cannot get config file %s Get err   #%v ", configFlag, err)
		}
		err = yaml.Unmarshal(yamlFile, &cfg)
		if err != nil {
			glog.Fatalf("Config parse error: %v", err)
		}
	}

	argError := func(s string, v ...interface{}) {
		//flag.PrintDefaults()
		glog.Errorf("Invalid Argument error: "+s, v...)
		os.Exit(-1)
	}

	if !cfg.flServer.flRunCli && cfg.flServer.flHTTPListenAddr == "" {
		argError("-http-listen-addr not specified or server.address not set")
	}
	if cfg.flServer.flConnTimeout <= 0 {
		argError("-connect-timeout or server.timeout must be greater than zero (specified: %v)", cfg.flServer.flConnTimeout)
	}
	if cfg.flProbeConfig.flRPCTimeout <= 0 {
		argError("-rpc-timeout or probe.config must be greater than zero (specified: %v)", cfg.flProbeConfig.flRPCTimeout)
	}
	if ( (cfg.flServer.flHTTPSTLSServerCert == "" && cfg.flServer.flHTTPSTLSServerKey != "") || (cfg.flServer.flHTTPSTLSServerCert != "" && cfg.flServer.flHTTPSTLSServerKey == "") ) {
		argError("must specify both -https-listen-cert and -https-listen-key")
	}
	if cfg.flServer.flHTTPSTLSVerifyCA == "" && cfg.flServer.flHTTPSTLSVerifyClient {
		argError("cannot specify -https-listen-ca if https-listen-verify is set (you need a trust CA for client certificate https auth)")
	}
	for path, config := range cfg.flProbeRequestConifg {
		if config.flGrpcServerAddr == "" {
			argError("-grpcaddr(hosts."+ path + ".address) not specified")
		}
		if !config.flGrpcTLS && config.flGrpcTLSNoVerify {
			argError("specified -grpc-tls-no-verify(hosts."+ path + ".tlsNoVerfiy) without specifying -grpctls(hosts."+ path + ".tlsEnabled")
		}
		if !config.flGrpcTLS && config.flGrpcTLSCACert != "" {
			argError("specified -grpc-ca-cert(hosts."+ path + ".tlsCaPath) without specifying -grpctls(hosts."+ path + ".tlsEnabled")
		}
		if !config.flGrpcTLS && config.flGrpcTLSClientCert != "" {
			argError("specified -grpc-client-cert(hosts."+ path + ".mtlsCertPath) without specifying -grpctls(hosts."+ path + ".tlsEnabled)")
		}
		if !config.flGrpcTLS && config.flGrpcSNIServerName != "" {
			argError("specified -grpc-sni-server-name(hosts."+ path + ".sniServerName) without specifying -grpctls(hosts."+ path + ".tlsEnabled)")
		}
		if config.flGrpcTLSClientCert != "" && config.flGrpcTLSClientKey == "" {
			argError("specified -grpc-client-cert(hosts."+ path + ".mtlsCertPath) without specifying -grpc-client-key(hosts."+ path + ".mtlsKeyPath)")
		}
		if config.flGrpcTLSClientCert == "" && config.flGrpcTLSClientKey != "" {
			argError("specified -grpc-client-key(hosts."+ path + ".mtlsKeyPath) without specifying -grpc-client-cert(hosts."+ path + ".mtlsCertPath")
		}
		if config.flGrpcTLSNoVerify && config.flGrpcTLSCACert != "" {
			argError("cannot specify -grpc-ca-cert(hosts."+ path + ".tlsCaPath) with -grpc-tls-no-verify(hosts."+ path + ".tlsNoVerify) (CA cert would not be used)")
		}
		if config.flGrpcTLSNoVerify && config.flGrpcSNIServerName != "" {
			argError("cannot specify -grpc-sni-server-name(hosts."+ path + ".sniServerName) with -grpc-tls-no-verify(hosts."+ path + ".tlsNoVerify) (server name would not be used)")
		}
	}

	glog.V(10).Infof("parsed options:")
	glog.V(10).Infof("> conn_timeout=%s rpc_timeout=%s",cfg.flServer.flConnTimeout, cfg.flProbeConfig.flRPCTimeout)
	glog.V(10).Infof(" http-listen-addr=%s ", cfg.flServer.flHTTPListenAddr)
	glog.V(10).Infof(" http-listen-path=%s ", cfg.flServer.flHTTPListenPath)
	glog.V(10).Infof(" https-listen-cert=%s ", cfg.flServer.flHTTPSTLSServerCert)
	glog.V(10).Infof(" https-listen-key=%s ", cfg.flServer.flHTTPSTLSServerKey)
	glog.V(10).Infof(" https-listen-verify=%v ", cfg.flServer.flHTTPSTLSVerifyClient)
	glog.V(10).Infof(" https-listen-ca=%s ", cfg.flServer.flHTTPSTLSVerifyCA)


	for path, config := range cfg.flProbeRequestConifg {
		glog.V(10).Infof(">(" + path + ") grpctls=%v", config.flGrpcTLS)
		glog.V(10).Infof("  > grpc-tls-no-verify=%v ", config.flGrpcTLSNoVerify)
		glog.V(10).Infof("  > grpc-ca-cert=%s", config.flGrpcTLSCACert)
		glog.V(10).Infof("  > grpc-client-cert=%s", config.flGrpcTLSClientCert)
		glog.V(10).Infof("  > grpc-client-key=%s", config.flGrpcTLSClientKey)
		glog.V(10).Infof("  > grpc-sni-server-name=%s", config.flGrpcSNIServerName)
	}
}

func buildGrpcCredentials(hostConfig ProbeRequestConfig) (credentials.TransportCredentials, error) {
	var tlsCfg tls.Config

	if hostConfig.flGrpcTLSClientCert != "" && hostConfig.flGrpcTLSClientKey != "" {
		keyPair, err := tls.LoadX509KeyPair(hostConfig.flGrpcTLSClientCert, hostConfig.flGrpcTLSClientKey)
		if err != nil {
			return nil, fmt.Errorf("failed to load tls client cert/key pair. error=%v", err)
		}
		tlsCfg.Certificates = []tls.Certificate{keyPair}
	}

	if hostConfig.flGrpcTLSNoVerify {
		tlsCfg.InsecureSkipVerify = true
	} else if hostConfig.flGrpcTLSCACert != "" {
		rootCAs := x509.NewCertPool()
		pem, err := ioutil.ReadFile(hostConfig.flGrpcTLSCACert)
		if err != nil {
			return nil, fmt.Errorf("failed to load root CA certificates from file (%s) error=%v", hostConfig.flGrpcTLSCACert, err)
		}
		if !rootCAs.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no root CA certs parsed from file %s", hostConfig.flGrpcTLSCACert)
		}
		tlsCfg.RootCAs = rootCAs
	}
	if hostConfig.flGrpcSNIServerName != "" {
		tlsCfg.ServerName = hostConfig.flGrpcSNIServerName
	}
	return credentials.NewTLS(&tlsCfg), nil
}

func checkService(ctx context.Context, hostConfig ProbeRequestConfig, serviceName string) (healthpb.HealthCheckResponse_ServingStatus, error) {

	glog.V(10).Infof("establishing connection")
	connStart := time.Now()
	dialCtx, dialCancel := context.WithTimeout(ctx, cfg.flServer.flConnTimeout)
	defer dialCancel()
	conn, err := grpc.DialContext(dialCtx, hostConfig.flGrpcServerAddr, hostConfig.opts...)
	if err != nil {
		if err == context.DeadlineExceeded {
			glog.Warningf("timeout: failed to connect %s service %s within %s", hostConfig.flGrpcServerAddr, cfg.flServer.flConnTimeout)
		} else {
			glog.Warningf("error: failed to connect service at %s: %+v", hostConfig.flGrpcServerAddr, err)
		}
		return healthpb.HealthCheckResponse_UNKNOWN, NewGrpcProbeError(StatusConnectionFailure, "StatusConnectionFailure")
	}
	connDuration := time.Since(connStart)
	defer conn.Close()
	glog.V(10).Infof("connection established %v", connDuration)

	rpcStart := time.Now()
	rpcCtx, rpcCancel := context.WithTimeout(ctx, cfg.flProbeConfig.flRPCTimeout)
	defer rpcCancel()

	glog.V(10).Infoln("Running HealthCheck for service: ", serviceName)

	resp, err := healthpb.NewHealthClient(conn).Check(rpcCtx, &healthpb.HealthCheckRequest{Service: serviceName})
	if err != nil {
		// first handle and return gRPC-level errors
		if stat, ok := status.FromError(err); ok && stat.Code() == codes.Unimplemented {
			glog.Warningf("error: this server does not implement the grpc health protocol (grpc.health.v1.Health)")
			return healthpb.HealthCheckResponse_UNKNOWN, NewGrpcProbeError(StatusUnimplemented, "StatusUnimplemented")
		} else if stat, ok := status.FromError(err); ok && stat.Code() == codes.DeadlineExceeded {
			glog.Warningf("error timeout: health rpc did not complete within ", cfg.flProbeConfig.flRPCTimeout)
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
		if config, cExist := cfg.flProbeRequestConifg[value]; cExist {
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


	for _, config := range cfg.flProbeRequestConifg {
		config.opts = []grpc.DialOption{}
		config.opts = append(config.opts, grpc.WithUserAgent(cfg.flProbeConfig.flUserAgent))
		config.opts = append(config.opts, grpc.WithBlock())
		if config.flGrpcTLS {
			creds, err := buildGrpcCredentials(config)
			if err != nil {
				glog.Fatalf("failed to initialize tls credentials. error=%v", err)
			}
			config.opts = append(config.opts, grpc.WithTransportCredentials(creds))
		} else {
			config.opts = append(config.opts, grpc.WithInsecure())
		}
	}

	if (cfg.flServer.flRunCli) {
		resp, err := checkService(context.Background(), cfg.flProbeRequestConifg["cli"], cfg.flServer.flServiceName)
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
			glog.Errorf("HealtCheck Probe Error: service %s failed with reason: %v",  cfg.flServer.flServiceName,  resp.String())
			os.Exit(StatusUnhealthy)
		} else {
			glog.Infof("%s %v",  cfg.flServer.flServiceName,  resp.String())
		}
	} else {

		tlsConfig := &tls.Config{}
		if (cfg.flServer.flHTTPSTLSVerifyClient) {
			caCert, err := ioutil.ReadFile(cfg.flServer.flHTTPSTLSVerifyCA)
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
			Addr: cfg.flServer.flHTTPListenAddr,
			TLSConfig: tlsConfig,
		}
		http2.ConfigureServer(srv, &http2.Server{})
		rtr := mux.NewRouter()
		rtr.HandleFunc(cfg.flServer.flHTTPListenPath + "/{host:.+}/{service:.+}", healthHandler).Methods("GET")
                rtr.HandleFunc(cfg.flServer.flHTTPListenPath + "/{host:.+}", healthHandler).Methods("GET")
                rtr.HandleFunc(cfg.flServer.flHTTPListenPath + "/", healthHandler).Methods("GET")
		http.Handle(cfg.flServer.flHTTPListenPath, rtr)

		var err error
		if (cfg.flServer.flHTTPSTLSServerCert != "" && cfg.flServer.flHTTPSTLSServerKey != "" ) {
			err = srv.ListenAndServeTLS(cfg.flServer.flHTTPSTLSServerCert, cfg.flServer.flHTTPSTLSServerKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil {
			glog.Fatalf("ListenAndServe Error: ", err)
		}
	}
}
