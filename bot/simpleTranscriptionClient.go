package main

import (
    "crypto/rand"
    "crypto/rsa"
    "crypto/x509"
    "encoding/pem"
    "fmt"
    "log"
    "net"
    "time"
)

// SimpleTranscriptionClient for the actual transcription service protocol
type SimpleTranscriptionClient struct {
    Host        string
    Port        int
    Secret      string
    Conn        net.Conn
    connected   bool
    serverPubKey *rsa.PublicKey
}

func NewSimpleTranscriptionClient(host string, port int, secret string) *SimpleTranscriptionClient {
    return &SimpleTranscriptionClient{
        Host:        host,
        Port:        port,
        Secret:      secret,
        connected:   false,
        serverPubKey: nil,
    }
}

func (stc *SimpleTranscriptionClient) Connect() error {
    address := fmt.Sprintf("%s:%d", stc.Host, stc.Port)
    log.Printf("Connecting to transcription service at %s", address)
    
    conn, err := net.Dial("tcp", address)
    if err != nil {
        log.Printf("ERROR: Failed to dial transcription service: %v", err)
        return fmt.Errorf("failed to connect to transcription service: %v", err)
    }
    
    stc.Conn = conn
    stc.connected = true
    
    log.Printf("TCP connection established to transcription service")
    
    // Set read timeout
    stc.Conn.SetReadDeadline(time.Now().Add(10 * time.Second))
    
    // 1. Receive server's public key
    buffer := make([]byte, 512)
    n, err := stc.Conn.Read(buffer)
    if err != nil {
        log.Printf("ERROR: Failed to receive server public key: %v", err)
        stc.Conn.Close()
        stc.connected = false
        return fmt.Errorf("failed to receive server public key: %v", err)
    }
    
    log.Printf("Received server public key (%d bytes)", n)
    
    // 2. Parse the public key (assuming PEM format)
    block, _ := pem.Decode(buffer[:n])
    if block == nil {
        log.Printf("ERROR: Failed to parse PEM block from server key")
        stc.Conn.Close()
        stc.connected = false
        return fmt.Errorf("failed to parse server public key")
    }
    
    pubKey, err := x509.ParsePKIXPublicKey(block.Bytes)
    if err != nil {
        log.Printf("ERROR: Failed to parse public key: %v", err)
        stc.Conn.Close()
        stc.connected = false
        return fmt.Errorf("failed to parse public key: %v", err)
    }
    
    rsaPubKey, ok := pubKey.(*rsa.PublicKey)
    if !ok {
        log.Printf("ERROR: Not an RSA public key")
        stc.Conn.Close()
        stc.connected = false
        return fmt.Errorf("not an RSA public key")
    }
    
    stc.serverPubKey = rsaPubKey
    log.Printf("Server public key parsed successfully")
    
    // 3. Generate proper AES key and IV (16, 24, or 32 bytes for AES)
    aesKey := make([]byte, 32) // 256-bit key
    aesIV := make([]byte, 16)  // 128-bit IV
    
    rand.Read(aesKey)
    rand.Read(aesIV)
    
    // 4. Create the proper authentication message format
    // Format: AUTH <secret>\nAES_KEY:<key>\nAES_IV:<iv>\n
    authMessage := fmt.Sprintf("AUTH %s\nAES_KEY:%x\nAES_IV:%x\n", 
        stc.Secret, aesKey, aesIV)
    
    log.Printf("Sending authentication message: %s", authMessage)
    
    // 5. Encrypt the message with server's public key
    encryptedAuth, err := rsa.EncryptPKCS1v15(rand.Reader, stc.serverPubKey, []byte(authMessage))
    if err != nil {
        log.Printf("ERROR: Failed to encrypt authentication: %v", err)
        stc.Conn.Close()
        stc.connected = false
        return fmt.Errorf("failed to encrypt authentication: %v", err)
    }
    
    log.Printf("Sending encrypted authentication (%d bytes)", len(encryptedAuth))
    
    // 6. Send encrypted authentication
    _, err = stc.Conn.Write(encryptedAuth)
    if err != nil {
        log.Printf("ERROR: Failed to send encrypted authentication: %v", err)
        stc.Conn.Close()
        stc.connected = false
        return fmt.Errorf("failed to send authentication: %v", err)
    }
    
    log.Printf("Encrypted authentication sent successfully")
    
    // Remove read timeout
    stc.Conn.SetReadDeadline(time.Time{})
    
    // Wait for authentication to complete
    time.Sleep(2 * time.Second)
    
    log.Printf("Transcription service connection established successfully!")
    return nil
}

func (stc *SimpleTranscriptionClient) Close() {
    if stc.Conn != nil {
        log.Printf("Closing transcription service connection")
        stc.Conn.Close()
    }
    stc.connected = false
}

func (stc *SimpleTranscriptionClient) IsConnected() bool {
    return stc.connected
}
