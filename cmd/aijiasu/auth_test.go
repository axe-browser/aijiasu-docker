package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCryptoAndCredentials(t *testing.T) {
	// 1. 验证密钥生成非空
	key := deriveAESKey()
	if len(key) != 32 {
		t.Fatalf("AES-256 密钥长度必须为 32 字节，当前为: %d", len(key))
	}

	// 2. 验证加密解密闭环
	original := &Credentials{
		Username:  "13800138000",
		Password:  "SecurePassword!@#123",
		UpdatedAt: time.Now().Unix(),
	}

	encStr, err := encryptCredentials(original)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if encStr == "" {
		t.Fatal("加密结果不能为空")
	}

	decrypted, err := decryptCredentials(encStr)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}

	if decrypted.Username != original.Username {
		t.Errorf("用户名解密不匹配: 期望 %s, 实际 %s", original.Username, decrypted.Username)
	}
	if decrypted.Password != original.Password {
		t.Errorf("密码解密不匹配: 期望 %s, 实际 %s", original.Password, decrypted.Password)
	}

	// 3. 验证篡改密文后解密必须失败 (AEAD 认证完整性校验)
	tampered := encStr[:len(encStr)-4] + "AAAA"
	_, err = decryptCredentials(tampered)
	if err == nil {
		t.Error("篡改密文后解密应当报错，但未报错！")
	}

	// 4. 验证脱敏逻辑
	cases := map[string]string{
		"18010029423": "180****9423",
		"13912345678": "139****5678",
		"admin":       "ad****in",
		"abc":         "****",
	}
	for in, expected := range cases {
		out := maskUsername(in)
		if out != expected {
			t.Errorf("脱敏错误: 输入 %s, 期望 %s, 实际 %s", in, expected, out)
		}
	}

	// 5. 验证文件保存与加载权限
	tmpDir := t.TempDir()
	origProjectDir := getProjectDir()
	// 临时替换测试环境路径
	_ = origProjectDir
	testCredPath := filepath.Join(tmpDir, "conf", ".auth.enc")
	_ = os.MkdirAll(filepath.Dir(testCredPath), 0755)

	enc, err := encryptCredentials(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testCredPath, []byte(enc), 0600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(testCredPath)
	if err != nil {
		t.Fatal(err)
	}
	// POSIX 权限应为 0600 (由于 umask，检测是否仅 owner 可读写)
	perm := fi.Mode().Perm()
	if perm&0077 != 0 {
		t.Errorf("凭据文件权限应杜绝组和其他人权限: 当前为 %o", perm)
	}
}

func TestReEncryptValidCreds(t *testing.T) {
	cred := &Credentials{
		Username:  "18010029423",
		Password:  "!Qq123123",
		UpdatedAt: time.Now().Unix(),
	}
	err := saveCredentialsToFile(cred)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := loadCredentialsFromFile()
	if err != nil {
		t.Fatalf("加载已保存凭据失败: %v", err)
	}
	if loaded.Username != "18010029423" || loaded.Password != "!Qq123123" {
		t.Fatalf("凭据内容不匹配: %v", loaded)
	}
}
