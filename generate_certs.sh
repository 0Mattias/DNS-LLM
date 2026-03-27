#!/bin/bash
# Generates a self-signed certificate for local testing and DoT tunneling.

echo "Generating self-signed certificate for LLM-over-DNS..."

cat <<EOF > cert.conf
[req]
default_bits = 4096
prompt = no
default_md = sha256
distinguished_name = dn
x509_extensions = v3_ext

[dn]
C = US
ST = State
L = City
O = AgenticDNS
OU = LLM
CN = llm.local

[v3_ext]
subjectAltName = @alt_names

[alt_names]
DNS.1 = llm.local
DNS.2 = localhost
IP.1 = 127.0.0.1
EOF

openssl req -new -x509 -nodes -days 3650 -config cert.conf -keyout key.pem -out cert.pem
rm cert.conf

echo "Certificates generated successfully:"
echo "- cert.pem"
echo "- key.pem"
