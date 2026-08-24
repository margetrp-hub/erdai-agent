package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type wechatWebhookCrypto struct {
	token     string
	key       []byte
	receiveID string
}

type wechatEncryptedEnvelope struct {
	XMLName xml.Name `xml:"xml"`
	Encrypt string   `xml:"Encrypt"`
}

type wechatInboundMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MessageType  string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
	MessageID    string   `xml:"MsgId"`
	PictureURL   string   `xml:"PicUrl"`
	MediaID      string   `xml:"MediaId"`
	Format       string   `xml:"Format"`
	Recognition  string   `xml:"Recognition"`
	AgentID      string   `xml:"AgentID"`
	Event        string   `xml:"Event"`
	EventKey     string   `xml:"EventKey"`
	Token        string   `xml:"Token"`
	OpenKFID     string   `xml:"OpenKfId"`
}

type wechatTextReply struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MessageType  string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
}

type wechatMediaReply struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MessageType  string   `xml:"MsgType"`
	Media        struct {
		MediaID string `xml:"MediaId"`
	} `xml:"Image"`
}

func newWechatWebhookCrypto(token, encodingAESKey, receiveID string) (*wechatWebhookCrypto, error) {
	return newWechatWebhookCryptoWithEmptyReceiveID(token, encodingAESKey, receiveID, false)
}

func newWechatJSONCrypto(token, encodingAESKey string) (*wechatWebhookCrypto, error) {
	return newWechatWebhookCryptoWithEmptyReceiveID(token, encodingAESKey, "", true)
}

func newWechatWebhookCryptoWithEmptyReceiveID(token, encodingAESKey, receiveID string, allowEmpty bool) (*wechatWebhookCrypto, error) {
	token = strings.TrimSpace(token)
	encodingAESKey = strings.TrimSpace(encodingAESKey)
	if token == "" || encodingAESKey == "" || (!allowEmpty && strings.TrimSpace(receiveID) == "") {
		return nil, errors.New("wechat token, encoding_aes_key and receive id are required")
	}
	key, err := base64.StdEncoding.DecodeString(encodingAESKey + strings.Repeat("=", (4-len(encodingAESKey)%4)%4))
	if err != nil || len(key) != 32 {
		return nil, errors.New("wechat encoding_aes_key must decode to 32 bytes")
	}
	return &wechatWebhookCrypto{token: token, key: key, receiveID: strings.TrimSpace(receiveID)}, nil
}

func (c *wechatWebhookCrypto) decryptMessage(encrypted, signature, timestamp, nonce string) ([]byte, error) {
	if !wechatVerifySignature(c.token, signature, timestamp, nonce, encrypted) {
		return nil, errors.New("wechat callback signature is invalid")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encrypted))
	if err != nil || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("wechat callback ciphertext is invalid")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, c.key[:aes.BlockSize]).CryptBlocks(plaintext, ciphertext)
	plaintext, err = wechatUnpad(plaintext)
	if err != nil || len(plaintext) < 20 {
		return nil, errors.New("wechat callback plaintext is invalid")
	}
	messageLength := int(binary.BigEndian.Uint32(plaintext[16:20]))
	if messageLength < 0 || 20+messageLength > len(plaintext) {
		return nil, errors.New("wechat callback message length is invalid")
	}
	message := plaintext[20 : 20+messageLength]
	receiveID := plaintext[20+messageLength:]
	if c.receiveID != "" && subtle.ConstantTimeCompare(receiveID, []byte(c.receiveID)) != 1 {
		return nil, errors.New("wechat callback receive id does not match")
	}
	return message, nil
}

func (c *wechatWebhookCrypto) encryptMessage(message []byte, timestamp, nonce string) (string, error) {
	if timestamp == "" {
		timestamp = strconv.FormatInt(time.Now().Unix(), 10)
	}
	if nonce == "" {
		nonce = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	randomPrefix := make([]byte, 16)
	if _, err := rand.Read(randomPrefix); err != nil {
		return "", err
	}
	plaintext := append([]byte{}, randomPrefix...)
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(message)))
	plaintext = append(plaintext, length...)
	plaintext = append(plaintext, message...)
	plaintext = append(plaintext, []byte(c.receiveID)...)
	plaintext = wechatPad(plaintext)
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, c.key[:aes.BlockSize]).CryptBlocks(ciphertext, plaintext)
	encrypted := base64.StdEncoding.EncodeToString(ciphertext)
	signature := wechatSignature(c.token, timestamp, nonce, encrypted)
	return fmt.Sprintf(
		"<xml><Encrypt><![CDATA[%s]]></Encrypt><MsgSignature><![CDATA[%s]]></MsgSignature><TimeStamp>%s</TimeStamp><Nonce><![CDATA[%s]]></Nonce></xml>",
		encrypted, signature, timestamp, nonce,
	), nil
}

func wechatParseEncryptedBody(body []byte) (string, error) {
	var envelope wechatEncryptedEnvelope
	if err := xml.Unmarshal(body, &envelope); err != nil || strings.TrimSpace(envelope.Encrypt) == "" {
		return "", errors.New("wechat encrypted XML is invalid")
	}
	return strings.TrimSpace(envelope.Encrypt), nil
}

func wechatParseInboundMessage(body []byte) (wechatInboundMessage, error) {
	var message wechatInboundMessage
	err := xml.Unmarshal(body, &message)
	if err != nil || strings.TrimSpace(message.FromUserName) == "" {
		return wechatInboundMessage{}, errors.New("wechat message XML is invalid")
	}
	return message, nil
}

func wechatVerifyPlainSignature(token, signature, timestamp, nonce string) bool {
	return wechatVerifySignature(token, signature, timestamp, nonce)
}

func wechatVerifySignature(token, signature string, values ...string) bool {
	expected := wechatSignature(token, values...)
	provided := strings.ToLower(strings.TrimSpace(signature))
	return len(provided) == len(expected) && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func wechatSignature(token string, values ...string) string {
	parts := append([]string{strings.TrimSpace(token)}, values...)
	sort.Strings(parts)
	digest := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(digest[:])
}

func wechatPad(value []byte) []byte {
	padding := 32 - len(value)%32
	return append(value, bytes.Repeat([]byte{byte(padding)}, padding)...)
}

func wechatUnpad(value []byte) ([]byte, error) {
	if len(value) == 0 {
		return nil, errors.New("wechat padding is empty")
	}
	padding := int(value[len(value)-1])
	if padding < 1 || padding > 32 || padding > len(value) {
		return nil, errors.New("wechat padding is invalid")
	}
	for _, item := range value[len(value)-padding:] {
		if int(item) != padding {
			return nil, errors.New("wechat padding is invalid")
		}
	}
	return value[:len(value)-padding], nil
}

func wechatTextReplyXML(message wechatInboundMessage, text string) ([]byte, error) {
	return xml.Marshal(wechatTextReply{
		ToUserName: message.FromUserName, FromUserName: message.ToUserName,
		CreateTime: time.Now().Unix(), MessageType: "text", Content: text,
	})
}

func wechatMediaReplyXML(message wechatInboundMessage, mediaType, mediaID string) ([]byte, error) {
	reply := wechatMediaReply{
		ToUserName: message.FromUserName, FromUserName: message.ToUserName,
		CreateTime: time.Now().Unix(), MessageType: mediaType,
	}
	reply.Media.MediaID = mediaID
	encoded, err := xml.Marshal(reply)
	if err != nil {
		return nil, err
	}
	if mediaType != "image" {
		encoded = bytes.Replace(encoded, []byte("<Image>"), []byte("<Voice>"), 1)
		encoded = bytes.Replace(encoded, []byte("</Image>"), []byte("</Voice>"), 1)
	}
	return encoded, nil
}
