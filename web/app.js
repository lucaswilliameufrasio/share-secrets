const createView = document.querySelector("#create-view");
const resultView = document.querySelector("#result-view");
const openView = document.querySelector("#open-view");
const status = document.querySelector("#status");
const languageSelect = document.querySelector("#language-select");

const messages = {
  "pt-BR": {
    title: "Compartilhar segredo",
    eyebrow: "COMPARTILHAMENTO TEMPORÁRIO",
    languageLabel: "Idioma",
    languageAriaLabel: "Selecionar idioma",
    headlineFirst: "Um segredo.",
    headlineSecond: "Uma leitura.",
    intro: "O conteúdo é cifrado neste navegador. O servidor recebe apenas dados cifrados e apaga o link depois da primeira abertura.",
    secretLabel: "Segredo",
    secretPlaceholder: "Cole aqui a credencial ou mensagem",
    createButton: "Criar link descartável",
    expiryHint: "Expira em 1 hora. Envie o link por um canal privado.",
    resultTitle: "Link pronto",
    resultDescription: "Copie e envie para a pessoa. Ele só pode ser aberto uma vez.",
    shareLinkLabel: "Link para compartilhar",
    copyButton: "Copiar link",
    openedTitle: "Segredo recebido",
    openedHint: "Este conteúdo foi removido do servidor e não poderá ser aberto novamente.",
    emptySecret: "Digite o segredo antes de criar o link.",
    createFailed: "Não foi possível criar o link. Tente novamente.",
    linkCreated: "Link criado. A chave de descriptografia está apenas no fragmento da URL.",
    claiming: "Buscando e descriptografando o segredo…",
    alreadyOpening: "Este segredo já está sendo aberto. Tente novamente em instantes.",
    unavailable: "Este link já foi usado, expirou ou não existe.",
    decryptFailed: "Não foi possível descriptografar. O segredo continua disponível para tentar novamente.",
    ackFailed: "O segredo foi descriptografado, mas não foi possível confirmar a leitura. Reabra o link em instantes.",
    openFailed: "Não foi possível descriptografar. O link continua disponível para tentar novamente.",
    copied: "Link copiado.",
    copyFailed: "Não foi possível copiar o link. Copie-o manualmente.",
  },
  en: {
    title: "Share a secret",
    eyebrow: "ONE-TIME SHARING",
    languageLabel: "Language",
    languageAriaLabel: "Select language",
    headlineFirst: "One secret.",
    headlineSecond: "One read.",
    intro: "The content is encrypted in this browser. The server only receives encrypted data and deletes the link after it is opened.",
    secretLabel: "Secret",
    secretPlaceholder: "Paste a credential or message",
    createButton: "Create one-time link",
    expiryHint: "Expires in 1 hour. Send the link through a private channel.",
    resultTitle: "Your link is ready",
    resultDescription: "Copy and send it to the recipient. It can only be opened once.",
    shareLinkLabel: "Shareable link",
    copyButton: "Copy link",
    openedTitle: "Secret received",
    openedHint: "This content was removed from the server and cannot be opened again.",
    emptySecret: "Enter the secret before creating a link.",
    createFailed: "Could not create the link. Please try again.",
    linkCreated: "Link created. The decryption key is only in the URL fragment.",
    claiming: "Retrieving and decrypting the secret…",
    alreadyOpening: "This secret is already being opened. Try again in a moment.",
    unavailable: "This link was used, expired, or does not exist.",
    decryptFailed: "Could not decrypt the secret. It is still available to try again.",
    ackFailed: "The secret was decrypted, but reading could not be confirmed. Reopen the link in a moment.",
    openFailed: "Could not decrypt the secret. The link is still available to try again.",
    copied: "Link copied.",
    copyFailed: "Could not copy the link. Please copy it manually.",
  },
};

function normalizeLanguage(language) {
  if (language.toLowerCase().startsWith("pt")) {
    return "pt-BR";
  }
  return "en";
}

function preferredLanguage() {
  const savedLanguage = localStorage.getItem("share-secrets-language");
  if (savedLanguage === "pt-BR" || savedLanguage === "en") {
    return savedLanguage;
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

function translate(key) {
  return messages[language][key] || messages.en[key] || key;
}

function setStatus(messageKey) {
  status.dataset.messageKey = messageKey;
  status.textContent = messageKey ? translate(messageKey) : "";
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
    status.textContent = translate(status.dataset.messageKey);
  }
}

languageSelect.addEventListener("change", () => {
  language = normalizeLanguage(languageSelect.value);
  localStorage.setItem("share-secrets-language", language);
  applyLanguage();
});
applyLanguage();

function encodeBase64Url(bytes) {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

function decodeBase64Url(value) {
  const base64 = value.replaceAll("-", "+").replaceAll("_", "/");
  const binary = atob(base64 + "=".repeat((4 - base64.length % 4) % 4));
  return Uint8Array.from(binary, (char) => char.charCodeAt(0));
}

async function createLink() {
  const secret = document.querySelector("#secret").value;
  if (!secret.trim()) {
    setStatus("emptySecret");
    return;
  }

  const keyBytes = crypto.getRandomValues(new Uint8Array(32));
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const proofBytes = crypto.getRandomValues(new Uint8Array(32));
  const proof = encodeBase64Url(proofBytes);
  const key = await crypto.subtle.importKey("raw", keyBytes, "AES-GCM", false, ["encrypt"]);
  const encryptedPayload = JSON.stringify({ secret, proof });
  const ciphertext = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, new TextEncoder().encode(encryptedPayload));
  const proofHash = encodeBase64Url(new Uint8Array(await crypto.subtle.digest("SHA-256", proofBytes)));
  const response = await fetch("/api/secrets", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ciphertext: encodeBase64Url(new Uint8Array(ciphertext)), iv: encodeBase64Url(iv), proofHash }),
  });
  if (!response.ok) {
    throw new Error(translate("createFailed"));
  }

  const { id } = await response.json();
  const fragment = `${id}.${encodeBase64Url(keyBytes)}`;
  const url = `${location.origin}/#${fragment}`;
  document.querySelector("#share-link").value = url;
  document.querySelector("#secret").value = "";
  createView.hidden = true;
  resultView.hidden = false;
  setStatus("linkCreated");
}

async function openLink() {
  const [id, encodedKey, extra] = location.hash.slice(1).split(".");
  if (!id || !encodedKey || extra) return;

  createView.hidden = true;
  setStatus("claiming");
  let claimToken = "";
  try {
    const response = await fetch(`/api/secrets/${encodeURIComponent(id)}`, { cache: "no-store" });
    if (response.status === 409) {
      throw new Error(translate("alreadyOpening"));
    }
    if (!response.ok) {
      throw new Error(translate("unavailable"));
    }
    const { ciphertext, iv, token } = await response.json();
    claimToken = token;
    const key = await crypto.subtle.importKey("raw", decodeBase64Url(encodedKey), "AES-GCM", false, ["decrypt"]);
    let payload;
    try {
      const plaintext = await crypto.subtle.decrypt({ name: "AES-GCM", iv: decodeBase64Url(iv) }, key, decodeBase64Url(ciphertext));
      payload = JSON.parse(new TextDecoder().decode(plaintext));
      if (typeof payload.secret !== "string" || typeof payload.proof !== "string" || decodeBase64Url(payload.proof).length !== 32) {
        throw new Error("invalid encrypted payload");
      }
    } catch {
      throw new Error(translate("decryptFailed"));
    }

    const acknowledgement = await fetch(`/api/secrets/${encodeURIComponent(id)}/ack`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token: claimToken, proof: payload.proof }),
      cache: "no-store",
    });
    if (!acknowledgement.ok) {
      throw new Error(translate("ackFailed"));
    }

    history.replaceState(null, "", location.pathname);
    document.querySelector("#opened-secret").textContent = payload.secret;
    openView.hidden = false;
    setStatus("");
  } catch (error) {
    if (claimToken) {
      try {
        await fetch(`/api/secrets/${encodeURIComponent(id)}/release`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ token: claimToken }),
          cache: "no-store",
        });
      } catch { /* The server lease will expire if release cannot reach it. */ }
    }
    history.replaceState(null, "", location.pathname);
    const errorMessage = error instanceof Error ? error.message : "";
    const knownErrors = ["alreadyOpening", "unavailable", "decryptFailed", "ackFailed"];
    let errorKey = "openFailed";
    for (const key of knownErrors) {
      if (translate(key) === errorMessage) {
        errorKey = key;
        break;
      }
    }
    setStatus(errorKey);
  }
}

document.querySelector("#create-button").addEventListener("click", () => {
  createLink().catch(() => { setStatus("createFailed"); });
});
document.querySelector("#copy-button").addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText(document.querySelector("#share-link").value);
    setStatus("copied");
  } catch {
    setStatus("copyFailed");
  }
});
openLink();
