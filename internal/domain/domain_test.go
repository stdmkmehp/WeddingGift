package domain

import "testing"

func TestNormalizeLoginName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"原文不变", "张伟", "张伟"},
		{"去除首尾空格", "  张伟  ", "张伟"},
		{"去除制表符与换行", "\t阿明\n", "阿明"},
		{"保留内部空格", "张 伟", "张 伟"},
		{"NFC 组合字符归并", "café", "café"},
		{"空白归一后为空", "   ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeLoginName(c.in); got != c.want {
				t.Fatalf("NormalizeLoginName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestIsValidAccessKeyCode(t *testing.T) {
	valid := []string{"12345678", "00000000", "99999999"}
	for _, s := range valid {
		if !IsValidAccessKeyCode(s) {
			t.Errorf("IsValidAccessKeyCode(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"", "1234567", "123456789", "1234 5678", "1234-5678",
		"abcdefgh", "１２３４５６７８", "1234567\n", " 12345678", "12345678 ",
	}
	for _, s := range invalid {
		if IsValidAccessKeyCode(s) {
			t.Errorf("IsValidAccessKeyCode(%q) = true, want false", s)
		}
	}
}

func TestIsValidGiftTypeKey(t *testing.T) {
	valid := []string{"travel_csj_first", "movie_voucher", "coffee_voucher", "a", "a1"}
	for _, s := range valid {
		if !IsValidGiftTypeKey(s) {
			t.Errorf("IsValidGiftTypeKey(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "Travel", "1abc", "a-b", "a b", "旅游年卡", "a_b_c_d_e_f_g_h_i_j_k_l_m_n_o_p_q_r_s"}
	for _, s := range invalid {
		if IsValidGiftTypeKey(s) {
			t.Errorf("IsValidGiftTypeKey(%q) = true, want false", s)
		}
	}
}
