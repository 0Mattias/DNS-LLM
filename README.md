# Agentic DNS-LLM

A robust client-server architecture in Go that enables secure, agentic interaction with Large Language Models (ie. Gemini) using the DNS protocol (DNS-over-TLS).
<img width="1084" height="729" alt="Screenshot 2026-03-27 at 6 56 25 AM" src="https://github.com/user-attachments/assets/207f56e9-5933-4e6f-ae07-cf834889e046" />


## Features
- **DNS Tunneling**: Custom stateful DNS tunneling protocol that uses optimized encoding to bypass DNS query size limits.
- **Agentic Architecture**: Built to support tool-calling, multi-turn state management, and autonomous decision-making all over DNS transport.
- **DNS-over-TLS (DoT)**: Encrypted DNS transport ensuring secure communication between the agentic DNS client and the server on port 853.
- **DNS-over-HTTPS (DoH)**: Alternative encrypted DNS transport wrapping DNS queries inside HTTP POST requests on standard port 443 (RFC 8484).
- **Terminal UI**: Highly polished, responsive, and markdown-capable terminal chat interface for a premium user experience.

## Setup

1. **Clone the Repository**
   ```bash
   git clone https://github.com/0Mattias/DNS-LLM.git
   cd DNS-LLM
   ```

2. **Generate TLS Certificates**
   Generate a self-signed certificate for local testing and DoT tunneling:
   ```bash
   ./generate_certs.sh
   ```
   This will create `cert.pem` and `key.pem` in the root directory.

3. **Configure the Environment**
   Create a `.env` file with your Gemini credentials along with toggles to enable the desired protocol (DoT or DoH):
   ```bash
   cat <<EOF > .env
   GEMINI_API_KEY="your_api_key_here"

   # Protocol Toggles (Set one to true)
   USE_DOT=false
   USE_DOH=true

   # Client endpoint (match protocol)
   # DoT: 127.0.0.1:853
   # DoH: https://127.0.0.1/dns-query
   DNS_SERVER_ADDR=https://127.0.0.1/dns-query
   INSECURE_SKIP_VERIFY=true

   # Server TLS Config
   TLS_CERT=cert.pem
   TLS_KEY=key.pem
   EOF
   ```

4. **Build the Project**
   ```bash
   # Build the server component
   go build -o server_bin ./server

   # Build the client component
   go build -o client_bin ./client
   ```

5. **Run**
   Start the server first (note: if `USE_DOH=true`, you may need `sudo` to bind to port 443):
   ```bash
   sudo ./server_bin
   ```
   
   In a separate terminal, start the client:
   ```bash
   ./client_bin
   ```

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
