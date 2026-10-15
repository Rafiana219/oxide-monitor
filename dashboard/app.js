
const API_BASE = "";

const elements = {
    total: document.getElementById("total-bots"),
    online: document.getElementById("online-bots"),
    offline: document.getElementById("offline-bots"),
    errors: document.getElementById("error-bots"),
    bots: document.getElementById("bots-table"),
    history: document.getElementById("history-table"),
    message: document.getElementById("message"),
    updated: document.getElementById("last-updated"),
    refresh: document.getElementById("refresh-btn"),
};

let busyBots = new Set();

function escapeHTML(value) {
    return String(value ?? "").replace(/[&<>"']/g, (char) => ({
        "&": "&amp;",
        "<": "&lt;",
        ">": "&gt;",
        '"': "&quot;",
        "'": "&#39;",
    })[char]);
}

function formatDate(value) {
    if (!value) return "—";

    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "—";

    return date.toLocaleString();
}

function formatUptime(value) {
    const seconds = Math.max(0, Number(value) || 0);
    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);

    if (days > 0) return `${days}d ${hours}h`;
    if (hours > 0) return `${hours}h ${minutes}m`;
    return `${minutes}m`;
}

function statusBadge(status) {
    const normalized = String(status || "unknown").toLowerCase();
    const allowed = [
        "online", "offline", "starting", "stopping",
        "error", "success", "failed", "running", "queued",
    ];
    const safeStatus = allowed.includes(normalized) ? normalized : "unknown";

    return `<span class="status status-${safeStatus}">${escapeHTML(normalized)}</span>`;
}

function showMessage(message, type = "success") {
    elements.message.textContent = message;
    elements.message.className = `message visible ${type}`;
}

function clearMessage() {
    elements.message.textContent = "";
    elements.message.className = "message";
}

async function apiRequest(path, options = {}) {
    const response = await fetch(`${API_BASE}${path}`, {
        ...options,
        headers: {
            ...(options.body ? { "Content-Type": "application/json" } : {}),
            ...(options.headers || {}),
        },
    });

    const raw = await response.text();
    let data = null;

    if (raw) {
        try {
            data = JSON.parse(raw);
        } catch {
            data = { message: raw };
        }
    }

    if (!response.ok) {
        const message = data?.message || data?.error || raw || response.statusText;
        throw new Error(`HTTP ${response.status}: ${message}`);
    }

    return data;
}

async function loadBots() {
    const data = await apiRequest("/api/bots");
    const bots = Array.isArray(data?.value) ? data.value : [];

    elements.total.textContent = bots.length;
    elements.online.textContent = bots.filter((bot) => bot.status === "online").length;
    elements.offline.textContent = bots.filter((bot) => bot.status === "offline").length;
    elements.errors.textContent = bots.filter((bot) => bot.status === "error").length;

    if (bots.length === 0) {
        elements.bots.innerHTML =
            '<tr><td colspan="6" class="empty">No bot agents found.</td></tr>';
        return;
    }

    elements.bots.innerHTML = bots.map((bot) => {
        const id = escapeHTML(bot.id);
        const name = escapeHTML(bot.name || bot.id);
        const device = escapeHTML(bot.deviceName || bot.device_name || "Unknown device");
        const cpu = Number(bot.cpuPercent ?? bot.cpu_percent ?? 0);
        const memory = Number(bot.memoryPercent ?? bot.memory_percent ?? 0);
        const uptime = bot.uptimeSeconds ?? bot.uptime_seconds ?? 0;
        const disabled = busyBots.has(String(bot.id)) ? "disabled" : "";

        return `
            <tr>
                <td>
                    <div class="bot-name">${name}</div>
                    <div class="bot-device">${device} · ${id}</div>
                </td>
                <td>${statusBadge(bot.status)}</td>
                <td>${Number.isFinite(cpu) ? cpu.toFixed(1) : "—"}%</td>
                <td>${Number.isFinite(memory) ? memory.toFixed(1) : "—"}%</td>
                <td>${formatUptime(uptime)}</td>
                <td>
                    <div class="actions">
                        <button class="action-btn" data-bot="${id}" data-command="start" ${disabled}>Start</button>
                        <button class="action-btn" data-bot="${id}" data-command="stop" ${disabled}>Stop</button>
                        <button class="action-btn" data-bot="${id}" data-command="restart" ${disabled}>Restart</button>
                        <button class="action-btn" data-bot="${id}" data-command="status" ${disabled}>Status</button>
                        <button class="action-btn" data-bot="${id}" data-history="true" ${disabled}>History</button>
                    </div>
                </td>
            </tr>
        `;
    }).join("");

    elements.updated.textContent = `Updated ${new Date().toLocaleTimeString()}`;
}


function renderHistory(commands) {
    if (commands.length === 0) {
        elements.history.innerHTML =
            '<tr><td colspan="6" class="empty">No commands recorded.</td></tr>';
        return;
    }

    elements.history.innerHTML = commands.map((command) => `
        <tr>
            <td>#${escapeHTML(command.id)}</td>
            <td>${escapeHTML(command.botId ?? command.bot_id ?? "—")}</td>
            <td>${escapeHTML(command.command)}</td>
            <td>${statusBadge(command.status)}</td>
            <td>${escapeHTML(command.requestedBy ?? command.requested_by ?? "—")}</td>
            <td>${escapeHTML(formatDate(command.createdAt ?? command.created_at))}</td>
        </tr>
    `).join("");
}

async function loadHistory(botId) {
    const data = await apiRequest(
        `/api/bots/${encodeURIComponent(botId)}/commands`
    );
    const commands = Array.isArray(data?.value) ? data.value : [];

    renderHistory(commands.map((command) => ({
        ...command,
        botId: command.botId ?? command.bot_id ?? botId,
    })));
}

async function loadAllHistory() {
    const botData = await apiRequest("/api/bots");
    const bots = Array.isArray(botData?.value) ? botData.value : [];

    const histories = await Promise.all(
        bots.map(async (bot) => {
            const data = await apiRequest(
                `/api/bots/${encodeURIComponent(bot.id)}/commands`
            );
            const commands = Array.isArray(data?.value) ? data.value : [];

            return commands.map((command) => ({
                ...command,
                botId: command.botId ?? command.bot_id ?? bot.id,
            }));
        })
    );

    const allCommands = histories.flat();

    allCommands.sort((a, b) =>
        new Date(b.createdAt ?? b.created_at).getTime() -
        new Date(a.createdAt ?? a.created_at).getTime()
    );

    renderHistory(allCommands);
}


async function sendCommand(botId, command) {
    if (busyBots.has(botId)) return;

    if (["stop", "restart"].includes(command)) {
        const confirmed = window.confirm(
            `Are you sure you want to ${command} ${botId}?`
        );
        if (!confirmed) return;
    }

    busyBots.add(botId);
    clearMessage();

    try {
        await apiRequest(`/api/bots/${encodeURIComponent(botId)}/commands`, {
            method: "POST",
            headers: { "X-Actor": "dashboard-user" },
            body: JSON.stringify({ command }),
        });

        showMessage(`Command "${command}" sent to ${botId}.`, "success");

        await loadBots();
        await loadAllHistory();
    } catch (error) {
        showMessage(`Failed to run ${command} on ${botId}: ${error.message}`, "error");
    } finally {
        busyBots.delete(botId);
        try {
            await loadBots();
        } catch {
            // Preserve the original command result message.
        }
    }
}


async function refreshDashboard() {
    elements.refresh.disabled = true;
    clearMessage();

    try {
        const syncResponse = await fetch(`${API_BASE}/api/bots/live`);

        if (!syncResponse.ok) {
            throw new Error(`Bot sync failed: HTTP ${syncResponse.status}`);
        }

        await loadBots();
        await loadAllHistory();
    } catch (error) {
        showMessage(`Could not load bots: ${error.message}`, "error");
        elements.bots.innerHTML =
            '<tr><td colspan="6" class="empty">Failed to load bot agents.</td></tr>';
    } finally {
        elements.refresh.disabled = false;
    }
}


elements.bots.addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-bot]");
    if (!button) return;

    const botId = button.dataset.bot;

    if (button.dataset.history === "true") {
        clearMessage();
        try {
            await loadHistory(botId);
            document.getElementById("commands").scrollIntoView({
                behavior: "smooth",
                block: "start",
            });
        } catch (error) {
            showMessage(`Could not load command history: ${error.message}`, "error");
        }
        return;
    }

    if (button.dataset.command) {
        await sendCommand(botId, button.dataset.command);
    }
});

elements.refresh.addEventListener("click", refreshDashboard);

refreshDashboard();

setInterval(() => {
    if (!elements.refresh.disabled) {
        refreshDashboard();
    }
}, 5000);