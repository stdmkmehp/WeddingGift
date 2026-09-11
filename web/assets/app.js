// 占位主页脚本：仅调用相对路径 healthz 验证访问链路，不含任何业务逻辑。
// 所有请求使用相对路径，兼容 /wedding/ 前缀，禁止域名根路径。

const healthState = document.getElementById("health-state");
const buildVersion = document.getElementById("build-version");
const serverTime = document.getElementById("server-time");

async function checkHealth() {
    try {
        const resp = await fetch("healthz", {
            method: "GET",
            headers: { Accept: "application/json" },
            cache: "no-store",
        });
        if (!resp.ok) {
            throw new Error("status " + resp.status);
        }
        const data = await resp.json();
        healthState.textContent = data.status === "ok" ? "正常" : "异常";
        healthState.dataset.state = data.status === "ok" ? "ok" : "error";
        buildVersion.textContent = data.version || "—";
        serverTime.textContent = data.time || "—";
    } catch (err) {
        healthState.textContent = "不可达";
        healthState.dataset.state = "error";
    }
}

checkHealth();
