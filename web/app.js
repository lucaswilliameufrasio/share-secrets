const createView = document.querySelector("#create-view");
const resultView = document.querySelector("#result-view");
const trackingView = document.querySelector("#tracking-view");
const openView = document.querySelector("#open-view");
const openedResult = document.querySelector("#opened-result");
const openedSecret = document.querySelector("#opened-secret");
const recoveryStatus = document.querySelector("#recovery-status");
const status = document.querySelector("#status");
const languageSelect = document.querySelector("#language-select");
const trackingList = document.querySelector("#tracked-links");
const trackingSummary = document.querySelector("#tracking-summary");
const metricsValues = document.querySelector("#metrics-values");
const trackedLinksKey = "share-secrets-tracked-links";
const maxTrackedLinks = 20;
const trackableStatuses = new Set(["active", "opening", "used", "expired"]);

const views = {
  create: createView,
  result: resultView,
  open: openView,
};

const messages = {
  "pt-BR": {
    title: "Compartilhar segredo",
    eyebrow: "COMPARTILHAMENTO TEMPORÁRIO",
    languageLabel: "Idioma",
    languageAriaLabel: "Selecionar idioma",
    headlineFirst: "Um segredo.",
    headlineSecond: "Uma leitura.",
    intro: "O conteúdo é cifrado neste navegador. O servidor recebe apenas dados cifrados e apaga o conteúdo após a confirmação da primeira leitura.",
    secretLabel: "Segredo",
    secretPlaceholder: "Cole aqui a credencial ou mensagem",
    ttlLabel: "Validade do link (minutos)",
    ttlPlaceholder: "Usar padrão do servidor",
    createButton: "Criar link descartável",
    expiryHint: "O padrão configurado no servidor é {duration}. Você também pode escolher de 1 a 1440 minutos.",
    resultTitle: "Link pronto",
    resultDescription: "Copie e envie para a pessoa. Ele só pode ser aberto uma vez.",
    shareLinkLabel: "Link para compartilhar",
    copyButton: "Copiar link",
    openedTitle: "Segredo recebido",
    openedHint: "Este conteúdo foi removido do servidor e não poderá ser aberto novamente.",
    emptySecret: "Digite o segredo antes de criar o link.",
    invalidTTL: "A validade deve ser um número inteiro entre 1 e 1440 minutos.",
    createFailed: "Não foi possível criar o link. Tente novamente.",
    linkCreated: "Link criado. A chave de descriptografia está apenas no fragmento da URL.",
    linkCreatedNotTracked: "Link criado, mas não foi possível salvar o acompanhamento neste navegador. Copie-o agora.",
    trackingFull: "Este navegador já acompanha 20 links. Remova um registro antes de criar outro.",
    claiming: "Buscando e descriptografando o segredo…",
    alreadyOpening: "Este segredo já está sendo aberto. Tente novamente em instantes.",
    unavailable: "Este link já foi usado, expirou ou não existe.",
    decryptFailed: "Não foi possível descriptografar. O segredo continua disponível para tentar novamente.",
    ackFailed: "O segredo foi descriptografado, mas não foi possível confirmar a leitura. Reabra o link em instantes.",
    openFailed: "Não foi possível abrir o segredo. Tente novamente.",
    releaseFailed: "Não foi possível liberar a reserva; ela expirará automaticamente em instantes.",
    copied: "Link copiado.",
    copyFailed: "Não foi possível copiar o link. Copie-o manualmente.",
    trackingTitle: "Links criados neste navegador",
    trackingDescription: "As chaves de descriptografia não são salvas aqui. O estado é atualizado automaticamente.",
    refreshButton: "Atualizar",
    trackingEmpty: "Nenhum link está sendo acompanhado.",
    trackingCount: "Acompanhando {count} links: {active} ativos, {opening} abrindo, {used} usados e {expired} expirados.",
    trackingStorageUnavailable: "O armazenamento local está indisponível; links novos não serão acompanhados após fechar esta página.",
    statusActive: "Disponível",
    statusOpening: "Sendo aberto",
    statusUsed: "Usado",
    statusExpired: "Expirado",
    statusUnavailable: "Sem dados no servidor",
    statusChecking: "Verificando…",
    createdAtLabel: "Criado",
    expiresAtLabel: "Expira",
    claimCountLabel: "Aberturas iniciadas",
    removeTrackingButton: "Remover",
    metricsTitle: "Métricas de uso",
    metricsDescription: "Totais operacionais são privados e reiniciam quando o servidor reinicia.",
    metricsTokenLabel: "Token de acesso às métricas",
    metricsTokenPlaceholder: "Informe o token configurado no servidor",
    loadMetricsButton: "Carregar métricas",
    metricsUnauthorized: "Token ausente ou inválido, ou métricas não habilitadas no servidor.",
    metricsUnavailable: "Não foi possível carregar as métricas.",
    metricCreated: "Links criados",
    metricClaims: "Aberturas iniciadas",
    metricConsumed: "Links usados",
    metricExpired: "Links expirados",
    metricActive: "Links ativos",
    metricOpening: "Aberturas em andamento",
    metricCapacity: "Capacidade ativa",
    metricTracked: "Registros acompanhados",
    metricRateLimited: "Criações limitadas",
    metricOverloaded: "Rejeições por concorrência",
    metricCapacityRejected: "Rejeições por capacidade",
    metricUptime: "Tempo ativo (segundos)",
    metricCleanupPending: "Limpeza do WAL pendente (0 ou 1)",
    pwaUnavailable: "Não foi possível ativar a instalação PWA neste navegador.",
    onlineOnly: "Este app precisa de conexão com o servidor; não funciona offline.",
  },
  en: {
    title: "Share a secret",
    eyebrow: "ONE-TIME SHARING",
    languageLabel: "Language",
    languageAriaLabel: "Select language",
    headlineFirst: "One secret.",
    headlineSecond: "One read.",
    intro: "The content is encrypted in this browser. The server receives only ciphertext and deletes it after the first read is confirmed.",
    secretLabel: "Secret",
    secretPlaceholder: "Paste a credential or message",
    ttlLabel: "Link validity (minutes)",
    ttlPlaceholder: "Use server default",
    createButton: "Create one-time link",
    expiryHint: "The server default is {duration}. You can also choose from 1 to 1440 minutes.",
    resultTitle: "Your link is ready",
    resultDescription: "Copy and send it to the recipient. It can only be opened once.",
    shareLinkLabel: "Shareable link",
    copyButton: "Copy link",
    openedTitle: "Secret received",
    openedHint: "This content was removed from the server and cannot be opened again.",
    emptySecret: "Enter the secret before creating a link.",
    invalidTTL: "Validity must be a whole number between 1 and 1440 minutes.",
    createFailed: "Could not create the link. Please try again.",
    linkCreated: "Link created. The decryption key is only in the URL fragment.",
    linkCreatedNotTracked: "Link created, but tracking could not be saved in this browser. Copy it now.",
    trackingFull: "This browser already tracks 20 links. Remove one before creating another.",
    claiming: "Retrieving and decrypting the secret…",
    alreadyOpening: "This secret is already being opened. Try again in a moment.",
    unavailable: "This link was used, expired, or does not exist.",
    decryptFailed: "Could not decrypt the secret. It is still available to try again.",
    ackFailed: "The secret was decrypted, but reading could not be confirmed. Reopen the link in a moment.",
    openFailed: "Could not open the secret. Please try again.",
    releaseFailed: "The reservation could not be released; it will expire automatically shortly.",
    copied: "Link copied.",
    copyFailed: "Could not copy the link. Please copy it manually.",
    trackingTitle: "Links created in this browser",
    trackingDescription: "Decryption keys are not saved here. Status refreshes automatically.",
    refreshButton: "Refresh",
    trackingEmpty: "No links are being tracked.",
    trackingCount: "Tracking {count} links: {active} active, {opening} opening, {used} used and {expired} expired.",
    trackingStorageUnavailable: "Local storage is unavailable; new links will not be tracked after this page closes.",
    statusActive: "Available",
    statusOpening: "Being opened",
    statusUsed: "Used",
    statusExpired: "Expired",
    statusUnavailable: "No server data",
    statusChecking: "Checking…",
    createdAtLabel: "Created",
    expiresAtLabel: "Expires",
    claimCountLabel: "Opening attempts",
    removeTrackingButton: "Remove",
    metricsTitle: "Usage metrics",
    metricsDescription: "Operational totals are private and reset when the server restarts.",
    metricsTokenLabel: "Metrics access token",
    metricsTokenPlaceholder: "Enter the token configured on the server",
    loadMetricsButton: "Load metrics",
    metricsUnauthorized: "Token is missing or invalid, or metrics are disabled on the server.",
    metricsUnavailable: "Could not load metrics.",
    metricCreated: "Links created",
    metricClaims: "Opening attempts",
    metricConsumed: "Links used",
    metricExpired: "Links expired",
    metricActive: "Active links",
    metricOpening: "Openings in progress",
    metricCapacity: "Active capacity",
    metricTracked: "Tracked records",
    metricRateLimited: "Rate-limited creations",
    metricOverloaded: "Concurrency rejections",
    metricCapacityRejected: "Capacity rejections",
    metricUptime: "Uptime (seconds)",
    metricCleanupPending: "WAL cleanup pending (0 or 1)",
    pwaUnavailable: "PWA installation could not be enabled in this browser.",
    onlineOnly: "This app needs a connection to the server; it does not work offline.",
  },
};

function normalizeLanguage(value) {
  if (value.toLowerCase().startsWith("pt")) {
    return "pt-BR";
  }
  return "en";
}

function preferredLanguage() {
  try {
    const savedLanguage = localStorage.getItem("share-secrets-language");
    if (savedLanguage === "pt-BR" || savedLanguage === "en") {
      return savedLanguage;
    }
  } catch {
    // Language preference is optional; continue with the browser preference.
  }
  const browserLanguages = navigator.languages.length > 0 ? navigator.languages : [navigator.language];
  for (const browserLanguage of browserLanguages) {
    if (browserLanguage.toLowerCase().startsWith("pt")) {
      return "pt-BR";
    }
  }
  return "en";
}

let language = preferredLanguage();
let activeView = "create";
let defaultTTLSeconds = 3600;
let trackingStorageAvailable = true;
let trackedLinks = loadTrackedLinks();

function translate(key) {
  return messages[language][key] || messages.en[key] || key;
}

function setStatus(messageKey) {
  status.dataset.messageKey = messageKey;
  status.textContent = messageKey ? translate(messageKey) : "";
}

function setView(view) {
  activeView = view;
  for (const [name, element] of Object.entries(views)) {
    element.hidden = name !== activeView;
  }
  trackingView.hidden = activeView === "open";
}

function canRefreshTrackedLinks() {
  return !trackingView.hidden && document.visibilityState === "visible";
}

function formatDuration(seconds) {
  if (seconds % 3600 === 0) {
    const hours = seconds / 3600;
    return language === "pt-BR" ? `${hours} h` : `${hours} hr`;
  }
  if (seconds % 60 === 0) {
    const minutes = seconds / 60;
    return language === "pt-BR" ? `${minutes} min` : `${minutes} min`;
  }
  return language === "pt-BR" ? `${seconds} s` : `${seconds} sec`;
}

function refreshExpiryHint() {
  document.querySelector("#expiry-hint").textContent = translate("expiryHint").replace("{duration}", formatDuration(defaultTTLSeconds));
}

function applyLanguage() {
  document.documentElement.lang = language;
  document.title = translate("title");
  languageSelect.value = language;
  languageSelect.setAttribute("aria-label", translate("languageAriaLabel"));

  for (const element of document.querySelectorAll("[data-i18n]")) {
    element.textContent = translate(element.dataset.i18n);
  }
  for (const element of document.querySelectorAll("[data-i18n-placeholder]")) {
    element.setAttribute("placeholder", translate(element.dataset.i18nPlaceholder));
  }
  for (const element of document.querySelectorAll("[data-i18n-aria-label]")) {
    element.setAttribute("aria-label", translate(element.dataset.i18nAriaLabel));
  }
  if (status.dataset.messageKey) {
    setStatus(status.dataset.messageKey);
  }
  refreshExpiryHint();
  renderTrackedLinks();
}

languageSelect.addEventListener("change", () => {
  language = normalizeLanguage(languageSelect.value);
  try {
    localStorage.setItem("share-secrets-language", language);
  } catch {
    // Language preference is optional and is not needed to use the app.
  }
  applyLanguage();
});
applyLanguage();

function encodeBase64Url(bytes) {
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

function decodeBase64Url(value) {
  if (!/^[A-Za-z0-9_-]+$/.test(value)) {
    throw new Error("invalid base64url value");
  }
  const base64 = value.replaceAll("-", "+").replaceAll("_", "/");
  const binary = atob(base64 + "=".repeat((4 - (base64.length % 4)) % 4));
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

function isTrackedLink(value) {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return false;
  }
  return typeof value.id === "string" && /^[A-Za-z0-9_-]{32}$/.test(value.id)
    && typeof value.trackingToken === "string" && /^[A-Za-z0-9_-]{43}$/.test(value.trackingToken)
    && typeof value.createdAt === "string" && Number.isFinite(Date.parse(value.createdAt))
    && typeof value.expiresAt === "string" && Number.isFinite(Date.parse(value.expiresAt));
}

function loadTrackedLinks() {
  try {
    const saved = localStorage.getItem(trackedLinksKey);
    if (!saved) {
      return [];
    }
    const parsed = JSON.parse(saved);
    if (!Array.isArray(parsed)) {
      return [];
    }
    return parsed.filter(isTrackedLink).slice(0, maxTrackedLinks).map((record) => ({ ...record, status: "checking", claimCount: 0 }));
  } catch {
    trackingStorageAvailable = false;
    return [];
  }
}

function saveTrackedLinks(records) {
  try {
    const persistentRecords = records.map((record) => ({
      id: record.id,
      trackingToken: record.trackingToken,
      createdAt: record.createdAt,
      expiresAt: record.expiresAt,
    }));
    localStorage.setItem(trackedLinksKey, JSON.stringify(persistentRecords));
    trackingStorageAvailable = true;
    return true;
  } catch {
    trackingStorageAvailable = false;
    return false;
  }
}

function formattedDate(value) {
  return new Intl.DateTimeFormat(language, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

function statusMessageKey(value) {
  if (trackableStatuses.has(value)) {
    return `status${value[0].toUpperCase()}${value.slice(1)}`;
  }
  return "statusUnavailable";
}

function renderTrackedLinks() {
  trackingList.replaceChildren();
  const counts = { active: 0, opening: 0, used: 0, expired: 0 };
  for (const record of trackedLinks) {
    if (Object.hasOwn(counts, record.status)) {
      counts[record.status]++;
    }
    const item = document.createElement("li");
    item.className = "tracked-link";

    const main = document.createElement("div");
    main.className = "tracked-link-main";
    const title = document.createElement("p");
    title.className = "tracked-link-title";
    title.textContent = `${translate(statusMessageKey(record.status))} · ${record.id.slice(0, 8)}`;
    const metadata = document.createElement("p");
    metadata.className = "tracked-link-meta";
    metadata.textContent = `${translate("createdAtLabel")}: ${formattedDate(record.createdAt)} · ${translate("expiresAtLabel")}: ${formattedDate(record.expiresAt)} · ${translate("claimCountLabel")}: ${record.claimCount || 0}`;
    main.append(title, metadata);

    const actions = document.createElement("div");
    actions.className = "tracked-link-actions";
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "secondary";
    remove.textContent = translate("removeTrackingButton");
    remove.setAttribute("aria-label", `${translate("removeTrackingButton")} ${record.id.slice(0, 8)}`);
    remove.addEventListener("click", () => {
      trackedLinks = trackedLinks.filter((candidate) => candidate.id !== record.id);
      saveTrackedLinks(trackedLinks);
      renderTrackedLinks();
    });
    actions.append(remove);
    item.append(main, actions);
    trackingList.append(item);
  }

  if (trackedLinks.length === 0) {
    trackingSummary.textContent = trackingStorageAvailable ? translate("trackingEmpty") : translate("trackingStorageUnavailable");
    return;
  }
  trackingSummary.textContent = translate("trackingCount")
    .replace("{count}", String(trackedLinks.length))
    .replace("{active}", String(counts.active))
    .replace("{opening}", String(counts.opening))
    .replace("{used}", String(counts.used))
    .replace("{expired}", String(counts.expired));
  if (!trackingStorageAvailable) {
    trackingSummary.textContent += ` ${translate("trackingStorageUnavailable")}`;
  }
}

async function refreshTrackedLinks() {
  if (trackedLinks.length === 0) {
    renderTrackedLinks();
    return;
  }
  await Promise.all(trackedLinks.map(async (record) => {
    try {
      const response = await fetch(`/api/secrets/${encodeURIComponent(record.id)}/status`, {
        headers: { Authorization: `Bearer ${record.trackingToken}` },
        cache: "no-store",
        redirect: "error",
      });
      if (!response.ok) {
        record.status = "unavailable";
        record.claimCount = 0;
        return;
      }
      const result = await response.json();
      if (typeof result !== "object" || result === null || result.id !== record.id
        || !trackableStatuses.has(result.status) || !Number.isSafeInteger(result.claimCount)
        || typeof result.createdAt !== "string" || !Number.isFinite(Date.parse(result.createdAt))
        || typeof result.expiresAt !== "string" || !Number.isFinite(Date.parse(result.expiresAt))) {
        record.status = "unavailable";
        record.claimCount = 0;
        return;
      }
      record.status = result.status;
      record.claimCount = result.claimCount;
      record.createdAt = result.createdAt;
      record.expiresAt = result.expiresAt;
    } catch {
      record.status = "unavailable";
      record.claimCount = 0;
    }
  }));
  renderTrackedLinks();
}

async function loadRuntimeConfig() {
  try {
    const response = await fetch("/api/config", { cache: "no-store", redirect: "error" });
    if (!response.ok) {
      return;
    }
    const config = await response.json();
    if (typeof config === "object" && config !== null && Number.isSafeInteger(config.defaultTTLSeconds)
      && Number.isSafeInteger(config.minimumTTLSeconds) && Number.isSafeInteger(config.maximumTTLSeconds)
      && config.minimumTTLSeconds >= 60 && config.maximumTTLSeconds <= 86_400
      && config.defaultTTLSeconds >= config.minimumTTLSeconds && config.defaultTTLSeconds <= config.maximumTTLSeconds) {
      defaultTTLSeconds = config.defaultTTLSeconds;
      refreshExpiryHint();
    }
  } catch {
    // The API remains authoritative if runtime defaults cannot be loaded.
  }
}

async function createLink() {
  const secretInput = document.querySelector("#secret");
  const secret = secretInput.value;
  if (!secret.trim()) {
    setStatus("emptySecret");
    return;
  }
  if (trackedLinks.length >= maxTrackedLinks) {
    setStatus("trackingFull");
    return;
  }

  const ttlMinutesValue = document.querySelector("#ttl-minutes").value.trim();
  let ttlSeconds;
  if (ttlMinutesValue !== "") {
    const ttlMinutes = Number(ttlMinutesValue);
    if (!Number.isInteger(ttlMinutes) || ttlMinutes < 1 || ttlMinutes > 1440) {
      setStatus("invalidTTL");
      return;
    }
    ttlSeconds = ttlMinutes * 60;
  }

  const createButton = document.querySelector("#create-button");
  createButton.disabled = true;
  try {
    const keyBytes = crypto.getRandomValues(new Uint8Array(32));
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const proofBytes = crypto.getRandomValues(new Uint8Array(32));
    const proof = encodeBase64Url(proofBytes);
    const key = await crypto.subtle.importKey("raw", keyBytes, "AES-GCM", false, ["encrypt"]);
    const encryptedPayload = JSON.stringify({ secret, proof });
    const ciphertext = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, new TextEncoder().encode(encryptedPayload));
    const proofHash = encodeBase64Url(new Uint8Array(await crypto.subtle.digest("SHA-256", proofBytes)));
    const payload = {
      ciphertext: encodeBase64Url(new Uint8Array(ciphertext)),
      iv: encodeBase64Url(iv),
      proofHash,
    };
    if (ttlSeconds !== undefined) {
      payload.ttlSeconds = ttlSeconds;
    }
    const response = await fetch("/api/secrets", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      cache: "no-store",
      redirect: "error",
    });
    if (!response.ok) {
      throw new Error("link creation failed");
    }
    const result = await response.json();
    if (typeof result !== "object" || result === null || typeof result.id !== "string"
      || !/^[A-Za-z0-9_-]{32}$/.test(result.id) || typeof result.trackingToken !== "string"
      || !/^[A-Za-z0-9_-]{43}$/.test(result.trackingToken) || typeof result.expiresAt !== "string"
      || !Number.isFinite(Date.parse(result.expiresAt))) {
      throw new Error("invalid creation response");
    }

    const fragment = `${result.id}.${encodeBase64Url(keyBytes)}`;
    const url = `${location.origin}/#${fragment}`;
    document.querySelector("#share-link").value = url;
    secretInput.value = "";
    const record = {
      id: result.id,
      trackingToken: result.trackingToken,
      createdAt: new Date().toISOString(),
      expiresAt: result.expiresAt,
      status: "active",
      claimCount: 0,
    };
    trackedLinks.unshift(record);
    const tracked = saveTrackedLinks(trackedLinks);
    if (!tracked) {
      trackedLinks = trackedLinks.slice(0, maxTrackedLinks);
    }
    setView("result");
    setStatus(tracked ? "linkCreated" : "linkCreatedNotTracked");
    renderTrackedLinks();
  } catch {
    setStatus("createFailed");
  } finally {
    createButton.disabled = false;
  }
}

async function openLink() {
  if (!location.hash) {
    return;
  }
  openedResult.hidden = true;
  openedSecret.textContent = "";
  recoveryStatus.textContent = "";
  const [id, encodedKey, extra] = location.hash.slice(1).split(".");
  history.replaceState(null, "", location.pathname);
  if (!id || !encodedKey || extra || !/^[A-Za-z0-9_-]{32}$/.test(id)) {
    setView("open");
    setStatus("openFailed");
    return;
  }
  try {
    if (decodeBase64Url(encodedKey).length !== 32) {
      setView("open");
      setStatus("openFailed");
      return;
    }
  } catch {
    setView("open");
    setStatus("openFailed");
    return;
  }

  setView("open");
  setStatus("claiming");
  let claimToken = "";
  let failureKey = "openFailed";
  try {
    const response = await fetch(`/api/secrets/${encodeURIComponent(id)}/claim`, {
      method: "POST",
      cache: "no-store",
      redirect: "error",
    });
    if (response.status === 409) {
      failureKey = "alreadyOpening";
      throw new Error("claim already in progress");
    }
    if (!response.ok) {
      failureKey = "unavailable";
      throw new Error("secret unavailable");
    }
    const result = await response.json();
    if (typeof result !== "object" || result === null || typeof result.ciphertext !== "string"
      || typeof result.iv !== "string" || typeof result.token !== "string" || !/^[A-Za-z0-9_-]{32}$/.test(result.token)) {
      throw new Error("invalid claim response");
    }
    claimToken = result.token;
    const key = await crypto.subtle.importKey("raw", decodeBase64Url(encodedKey), "AES-GCM", false, ["decrypt"]);
    let payload;
    try {
      const plaintext = await crypto.subtle.decrypt({ name: "AES-GCM", iv: decodeBase64Url(result.iv) }, key, decodeBase64Url(result.ciphertext));
      payload = JSON.parse(new TextDecoder().decode(plaintext));
      if (typeof payload.secret !== "string" || typeof payload.proof !== "string" || decodeBase64Url(payload.proof).length !== 32) {
        throw new Error("invalid encrypted payload");
      }
    } catch {
      failureKey = "decryptFailed";
      throw new Error("decryption failed");
    }

    const acknowledgement = await fetch(`/api/secrets/${encodeURIComponent(id)}/ack`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token: claimToken, proof: payload.proof }),
      cache: "no-store",
      redirect: "error",
    });
    if (!acknowledgement.ok) {
      failureKey = "ackFailed";
      throw new Error("acknowledgement failed");
    }

    history.replaceState(null, "", location.pathname);
    openedSecret.textContent = payload.secret;
    openedResult.hidden = false;
    setView("open");
    setStatus("");
  } catch {
    if (claimToken) {
      try {
        const release = await fetch(`/api/secrets/${encodeURIComponent(id)}/release`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ token: claimToken }),
          cache: "no-store",
          redirect: "error",
        });
        if (!release.ok) {
          recoveryStatus.textContent = translate("releaseFailed");
        }
      } catch {
        recoveryStatus.textContent = translate("releaseFailed");
      }
    }
    history.replaceState(null, "", location.pathname);
    setView("open");
    setStatus(failureKey);
  }
}

function renderMetrics(result) {
  const rows = [
    ["metricCreated", result.createdTotal],
    ["metricClaims", result.claimsTotal],
    ["metricConsumed", result.consumedTotal],
    ["metricExpired", result.expiredTotal],
    ["metricActive", result.active],
    ["metricOpening", result.opening],
    ["metricCapacity", result.activeCapacity],
    ["metricTracked", result.tracked],
    ["metricRateLimited", result.rateLimitedTotal],
    ["metricOverloaded", result.overloadedTotal],
    ["metricCapacityRejected", result.capacityRejectedTotal],
    ["metricUptime", result.uptimeSeconds],
    ["metricCleanupPending", result.cleanupPending],
  ];
  metricsValues.replaceChildren();
  for (const [labelKey, value] of rows) {
    const cell = document.createElement("div");
    const label = document.createElement("dt");
    label.textContent = translate(labelKey);
    const data = document.createElement("dd");
    data.textContent = String(Number.isSafeInteger(value) ? value : 0);
    cell.append(label, data);
    metricsValues.append(cell);
  }
  metricsValues.hidden = false;
}

async function loadMetrics() {
  const input = document.querySelector("#metrics-token");
  const metricsStatus = document.querySelector("#metrics-status");
  const token = input.value;
  input.value = "";
  metricsValues.hidden = true;
  metricsStatus.textContent = "";
  if (!token) {
    metricsStatus.textContent = translate("metricsUnauthorized");
    return;
  }
  try {
    const response = await fetch("/api/metrics", {
      headers: { Authorization: `Bearer ${token}` },
      cache: "no-store",
      redirect: "error",
    });
    if (!response.ok) {
      metricsStatus.textContent = response.status === 401 || response.status === 404
        ? translate("metricsUnauthorized")
        : translate("metricsUnavailable");
      return;
    }
    const result = await response.json();
    if (typeof result !== "object" || result === null) {
      throw new Error("invalid metrics response");
    }
    renderMetrics(result);
  } catch {
    metricsStatus.textContent = translate("metricsUnavailable");
  }
}

async function registerPWA() {
  if (!("serviceWorker" in navigator)) {
    document.querySelector("#pwa-status").textContent = translate("pwaUnavailable");
    return;
  }
  try {
    await navigator.serviceWorker.register("/service-worker.js", { scope: "/" });
  } catch {
    document.querySelector("#pwa-status").textContent = translate("pwaUnavailable");
  }
}

document.querySelector("#create-button").addEventListener("click", () => {
  createLink().catch(() => {
    setStatus("createFailed");
  });
});
document.querySelector("#copy-button").addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText(document.querySelector("#share-link").value);
    setStatus("copied");
  } catch {
    setStatus("copyFailed");
  }
});
document.querySelector("#refresh-links").addEventListener("click", () => {
  refreshTrackedLinks().catch(() => {
    trackingSummary.textContent = translate("statusUnavailable");
  });
});
document.querySelector("#load-metrics").addEventListener("click", () => {
  loadMetrics().catch(() => {
    document.querySelector("#metrics-status").textContent = translate("metricsUnavailable");
  });
});

loadRuntimeConfig();
if (location.hash) {
  openLink();
} else {
  refreshTrackedLinks();
}
registerPWA();
window.setInterval(() => {
  if (canRefreshTrackedLinks()) {
    refreshTrackedLinks();
  }
}, 30_000);
document.addEventListener("visibilitychange", () => {
  if (canRefreshTrackedLinks()) {
    refreshTrackedLinks();
  }
});
