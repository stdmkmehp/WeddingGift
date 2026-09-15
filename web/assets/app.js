// WeddingGift 单页状态机（产品架构 §5/§9/§12，M5 WG-503～WG-505）。
//
// 纪律：
// - 全部请求使用相对路径，兼容 /wedding/ 前缀，禁止域名根路径；
// - 动态内容一律走 textContent，不使用 innerHTML（防数据注入）；
// - 礼品凭证明文（激活码/卡号/密码）只保存在内存中，刷新/退出/会话失效即清空；
// - 不使用 localStorage / IndexedDB / Service Worker 保存会话与礼品凭证。

const $ = (id) => document.getElementById(id);

const views = {
    loading: $("view-loading"),
    login: $("view-login"),
    gifts: $("view-gifts"),
    error: $("view-error"),
};

// 服务文案（bootstrap 下发；bootstrap 失败时保留 HTML 中的占位默认）。
let serviceTexts = null;

// 当前会话数据（含礼品摘要）；凭证明文只存在 revealedCodes 内存表中（gift.id -> {code, secret}）。
let currentSession = null;
const revealedCodes = new Map();

function showView(name) {
    for (const [key, el] of Object.entries(views)) {
        el.hidden = key !== name;
    }
    // 供背景层随视图切换：合照背景仅登录态出现，坠落花瓣全局飘落。
    document.body.dataset.view = name;
    // 登录后新人称呼与婚期作为落款收入感谢信正文，顶部抬头不再出现。
    $("site-header").hidden = name === "gifts";
    // 登录欢迎语仅在登录态展示，避免在加载/异常态闪现。
    $("home-intro").hidden = name !== "login";
}

// ---------- 网络 ----------

// api 统一发起 JSON 请求；返回 { status, body }，网络层失败时抛出异常。
async function api(path, options = {}) {
    const resp = await fetch(path, {
        method: options.method || "GET",
        headers: options.body ? { "Content-Type": "application/json" } : undefined,
        body: options.body ? JSON.stringify(options.body) : undefined,
        cache: "no-store",
        credentials: "same-origin",
    });
    let body = null;
    try {
        body = await resp.json();
    } catch {
        body = null;
    }
    return { status: resp.status, body };
}

// ---------- 文案与页脚 ----------

function applyServiceTexts(service) {
    serviceTexts = service;
    const names = service.coupleNames || "赵伟 & 刘子璇";
    const date = service.weddingDate || "";
    $("couple-names").textContent = names;
    $("wedding-date").textContent = date;
    $("signature-names").textContent = names;
    $("signature-date").textContent = date;
    $("home-intro").textContent = service.homeIntro || "";
    $("login-hint").textContent = service.loginHint || "";
    $("privacy-note").textContent = service.privacyNote || "";
    $("contact-hint").textContent = service.contactHint || "";
    const icp = $("icp-link");
    icp.textContent = service.icpFilingText || "";
    // 备案链接只接受 https，非法配置时回退到默认站点。
    icp.href = typeof service.icpFilingUrl === "string" && service.icpFilingUrl.startsWith("https://")
        ? service.icpFilingUrl
        : "https://beian.miit.gov.cn/";
}

// ---------- 异常态（WG-505） ----------

function showErrorView(message) {
    const fallback = serviceTexts?.maintenanceText || "页面维护中，请稍后再试。";
    $("error-text").textContent = message || fallback;
    showView("error");
}

$("error-retry").addEventListener("click", () => {
    showView("loading");
    bootstrap();
});

// ---------- 初始化 ----------

async function bootstrap() {
    try {
        const { status, body } = await api("api/bootstrap");
        if (status !== 200 || !body || !body.service) {
            showErrorView();
            return;
        }
        applyServiceTexts(body.service);
        if (body.session) {
            enterGiftsView(body.session);
        } else {
            showView("login");
            $("login-name").focus();
        }
    } catch {
        showErrorView();
    }
}

// ---------- 登录（WG-503） ----------

const loginForm = $("login-form");
const loginSubmit = $("login-submit");
const loginError = $("login-error");

function showLoginError(message) {
    if (message) {
        loginError.textContent = message;
        loginError.hidden = false;
    } else {
        loginError.textContent = "";
        loginError.hidden = true;
    }
}

loginForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    showLoginError(null);

    const name = $("login-name").value.trim();
    const key = $("login-key").value;
    // 公开格式规则就地拦截，不发请求（产品架构 §12）。
    if (name === "") {
        showLoginError("请填写你的名字");
        $("login-name").focus();
        return;
    }
    if (!/^[0-9]{8}$/.test(key)) {
        showLoginError("卡片密钥是卡片上的 8 位数字，请核对后再试");
        $("login-key").focus();
        return;
    }

    // 提交防重复 + 加载态。
    loginSubmit.disabled = true;
    loginSubmit.textContent = "正在打开…";
    try {
        const { status, body } = await api("api/auth/login", {
            method: "POST",
            body: { loginName: name, keyCode: key },
        });
        if (status === 200 && body && body.session) {
            $("login-key").value = "";
            enterGiftsView(body.session);
            return;
        }
        // 400 格式错误 / 401 统一鉴权失败 / 429 限流：文案由后端统一给出。
        showLoginError(body?.error?.message || "服务暂时不可用，请稍后再试");
    } catch {
        showLoginError("网络似乎不太稳定，请稍后重试");
    } finally {
        loginSubmit.disabled = false;
        loginSubmit.textContent = "查看礼物";
    }
});

// ---------- 同行组欢迎与多礼品（WG-504） ----------

function enterGiftsView(session) {
    currentSession = session;
    clearAllRevealed();

    $("salutation").textContent = "致 " + session.salutation;
    $("thanks-message").textContent = session.thanksMessage || "";
    const custom = $("custom-message");
    if (session.customMessage) {
        custom.textContent = session.customMessage;
        custom.hidden = false;
    } else {
        custom.textContent = "";
        custom.hidden = true;
    }
    const gifts = Array.isArray(session.gifts) ? session.gifts : [];
    $("gift-count").textContent = `一共为你准备了 ${gifts.length} 份礼物`;

    const list = $("gift-list");
    list.replaceChildren();
    for (const gift of gifts) {
        list.appendChild(buildGiftCard(gift));
    }
    showView("gifts");
}

// buildGiftCard 用 DOM API 构建一张礼品卡；所有数据文本走 textContent。
// 凭证形态由 credentialMode 决定：code 为单一激活码；card_secret 为卡号+密码两行。
function buildGiftCard(gift) {
    const card = document.createElement("li");
    card.className = "gift-card";
    if (typeof gift.themeVariant === "string" && gift.themeVariant !== "") {
        card.dataset.theme = gift.themeVariant;
    }

    const title = document.createElement("h2");
    title.className = "gift-title";
    title.textContent = gift.title;
    card.appendChild(title);

    if (gift.subtitle) {
        const subtitle = document.createElement("p");
        subtitle.className = "gift-subtitle";
        subtitle.textContent = gift.subtitle;
        card.appendChild(subtitle);
    }

    const isCardSecret = gift.credentialMode === "card_secret";
    const revealLabel = isCardSecret ? "显示卡号密码" : "显示激活码";

    // 凭证区遮蔽态即按形态渲染骨架：card_secret 为带标签双行，各行自带复制按钮。
    const codeRow = document.createElement("div");
    codeRow.className = "gift-code-row";
    // fields: [{ key: "code"|"secret", codeEl, copyBtn(仅 card_secret 行内) }]
    const fields = [];
    const buildField = (key, labelText) => {
        const row = document.createElement("div");
        row.className = "gift-credential";
        if (labelText) {
            const label = document.createElement("span");
            label.className = "gift-credential-label";
            label.textContent = labelText;
            row.appendChild(label);
        }
        const codeEl = document.createElement("span");
        codeEl.className = "gift-code";
        codeEl.dataset.state = "masked";
        codeEl.textContent = "••••••••";
        row.appendChild(codeEl);
        const field = { key, codeEl, copyBtn: null };
        if (isCardSecret) {
            const btn = document.createElement("button");
            btn.type = "button";
            btn.className = "btn btn-secondary btn-mini";
            btn.textContent = "复制";
            btn.hidden = true;
            row.appendChild(btn);
            field.copyBtn = btn;
        }
        codeRow.appendChild(row);
        fields.push(field);
    };
    buildField("code", isCardSecret ? "卡号" : "");
    if (isCardSecret) {
        buildField("secret", "密码");
    }
    card.appendChild(codeRow);

    const actions = document.createElement("div");
    actions.className = "gift-actions";
    const revealBtn = document.createElement("button");
    revealBtn.type = "button";
    revealBtn.className = "btn btn-secondary";
    revealBtn.textContent = revealLabel;
    // 整码复制按钮仅 code 模式使用；card_secret 卡号/密码各有行内复制按钮。
    const copyBtn = document.createElement("button");
    copyBtn.type = "button";
    copyBtn.className = "btn btn-secondary";
    copyBtn.textContent = "复制";
    copyBtn.hidden = true;
    const hideBtn = document.createElement("button");
    hideBtn.type = "button";
    hideBtn.className = "btn btn-ghost";
    hideBtn.textContent = "隐藏";
    hideBtn.hidden = true;
    actions.append(revealBtn, copyBtn, hideBtn);
    card.appendChild(actions);

    const feedback = document.createElement("p");
    feedback.className = "copy-feedback";
    feedback.hidden = true;
    card.appendChild(feedback);

    const cardError = document.createElement("p");
    cardError.className = "card-error";
    cardError.hidden = true;
    card.appendChild(cardError);

    const meta = document.createElement("div");
    meta.className = "gift-meta";
    if (gift.instructions) {
        const instructions = document.createElement("p");
        instructions.textContent = gift.instructions;
        meta.appendChild(instructions);
    }
    if (gift.validityText) {
        const validity = document.createElement("p");
        validity.textContent = gift.validityText;
        meta.appendChild(validity);
    }
    // 兑换入口按契约仅两种形态（契约已保证，前端再兜底一次）：
    // HTTPS 链接渲染为可点链接；小程序口令（#小程序://…）不是可点 URL，
    // 渲染为复制按钮，引导用户粘贴到微信聊天后点击打开。
    if (typeof gift.redemptionUrl === "string" && gift.redemptionUrl.startsWith("https://")) {
        const link = document.createElement("a");
        link.href = gift.redemptionUrl;
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        link.textContent = "前往官方兑换";
        meta.appendChild(link);
    } else if (typeof gift.redemptionUrl === "string" && gift.redemptionUrl.startsWith("#小程序://")) {
        const mpBtn = document.createElement("button");
        mpBtn.type = "button";
        mpBtn.className = "btn btn-secondary";
        mpBtn.textContent = "复制小程序兑换口令";
        const mpFeedback = document.createElement("p");
        mpFeedback.className = "copy-feedback";
        mpFeedback.hidden = true;
        mpBtn.addEventListener("click", async () => {
            mpFeedback.hidden = true;
            // 剪贴板 API 可用（需安全上下文）时直接复制；否则选中口令原文供长按。
            if (window.isSecureContext && navigator.clipboard && navigator.clipboard.writeText) {
                try {
                    await navigator.clipboard.writeText(gift.redemptionUrl);
                    mpFeedback.textContent = "已复制口令，粘贴到微信任意聊天发送后，点击即可打开小程序";
                    mpFeedback.hidden = false;
                    return;
                } catch {
                    // 落入降级分支（微信 WebView 等无权限场景）。
                }
            }
            mpFeedback.textContent = gift.redemptionUrl;
            mpFeedback.hidden = false;
            selectCodeText(mpFeedback);
        });
        meta.append(mpBtn, mpFeedback);
    }
    card.appendChild(meta);

    // copyValue 统一复制入口：剪贴板 API 可用（需安全上下文）时直接复制；
    // 否则选中目标文本，引导长按手动复制。
    const copyValue = async (value, targetEl, okText, fallbackText) => {
        feedback.hidden = true;
        if (window.isSecureContext && navigator.clipboard && navigator.clipboard.writeText) {
            try {
                await navigator.clipboard.writeText(value);
                feedback.textContent = okText;
                feedback.hidden = false;
                return;
            } catch {
                // 落入降级分支（微信 WebView 等无权限场景）。
            }
        }
        selectCodeText(targetEl);
        feedback.textContent = fallbackText;
        feedback.hidden = false;
    };

    const setRevealed = (code, secret) => {
        revealedCodes.set(gift.id, { code, secret });
        for (const f of fields) {
            f.codeEl.textContent = f.key === "secret" ? secret : code;
            f.codeEl.dataset.state = "revealed";
            if (f.copyBtn) {
                f.copyBtn.hidden = false;
            }
        }
        revealBtn.hidden = true;
        copyBtn.hidden = isCardSecret;
        hideBtn.hidden = false;
        (isCardSecret ? fields[0].copyBtn : copyBtn).focus();
    };
    const setMasked = () => {
        revealedCodes.delete(gift.id);
        for (const f of fields) {
            f.codeEl.textContent = "••••••••";
            f.codeEl.dataset.state = "masked";
            if (f.copyBtn) {
                f.copyBtn.hidden = true;
            }
        }
        revealBtn.hidden = false;
        copyBtn.hidden = true;
        hideBtn.hidden = true;
        feedback.hidden = true;
    };

    revealBtn.addEventListener("click", async () => {
        cardError.hidden = true;
        revealBtn.disabled = true;
        revealBtn.textContent = "正在显示…";
        try {
            const { status, body } = await api("api/gifts/reveal", {
                method: "POST",
                body: { giftId: gift.id },
            });
            if (status === 200 && body && typeof body.code === "string") {
                setRevealed(body.code, typeof body.secret === "string" ? body.secret : "");
                return;
            }
            if (status === 401) {
                sessionExpired();
                return;
            }
            cardError.textContent = body?.error?.message || "礼物信息暂时无法读取，请稍后再试";
            cardError.hidden = false;
        } catch {
            cardError.textContent = "网络似乎不太稳定，请稍后重试";
            cardError.hidden = false;
        } finally {
            revealBtn.disabled = false;
            revealBtn.textContent = revealLabel;
        }
    });

    copyBtn.addEventListener("click", () => {
        const revealed = revealedCodes.get(gift.id);
        if (!revealed) {
            return;
        }
        copyValue(revealed.code, fields[0].codeEl, "已复制，快去兑换吧", "请长按激活码进行复制");
    });

    // card_secret 行内复制：卡号与密码分别复制，便于逐项粘贴到兑换表单。
    for (const f of fields) {
        if (!f.copyBtn) {
            continue;
        }
        f.copyBtn.addEventListener("click", () => {
            const revealed = revealedCodes.get(gift.id);
            if (!revealed) {
                return;
            }
            const label = f.key === "secret" ? "密码" : "卡号";
            copyValue(revealed[f.key], f.codeEl, `已复制${label}`, `请长按${label}进行复制`);
        });
    }

    hideBtn.addEventListener("click", setMasked);

    return card;
}

// selectCodeText 选中明文凭证文本，供剪贴板不可用时手动复制。
function selectCodeText(codeEl) {
    const selection = window.getSelection();
    if (!selection) {
        return;
    }
    const range = document.createRange();
    range.selectNodeContents(codeEl);
    selection.removeAllRanges();
    selection.addRange(range);
}

// clearAllRevealed 清空全部凭证明文（退出/会话失效/重新进入礼品态前调用）。
function clearAllRevealed() {
    revealedCodes.clear();
    $("gift-list").replaceChildren();
}

// sessionExpired 会话失效：清空全部明文并回到登录态（产品架构 §5.3/§12）。
function sessionExpired() {
    currentSession = null;
    clearAllRevealed();
    showView("login");
    showLoginError("登录状态已失效，请重新登录");
}

// ---------- 退出 ----------

$("logout-btn").addEventListener("click", async () => {
    try {
        await api("api/auth/logout", { method: "POST" });
    } catch {
        // 退出请求失败不阻塞本地清理；会话在服务端有过期兜底。
    }
    currentSession = null;
    clearAllRevealed();
    showView("login");
    showLoginError("已退出登录");
});

// ---------- 启动 ----------

bootstrap();
