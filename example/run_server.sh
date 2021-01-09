#!/bin/bash
set -e
pushd src
go build  grpc_server.go
popd
src/grpc_server -tlsCert certs/grpc_server_crt.pem -tlsKey certs/grpc_server_key.pem -grpcport 127.0.0.1:5051

