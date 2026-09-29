const createView = document.querySelector("#create-view");
const resultView = document.querySelector("#result-view");
const openView = document.querySelector("#open-view");
const status = document.querySelector("#status");

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
    status.textContent = "Digite o segredo antes de criar o link.";
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
  if (!response.ok) throw new Error("Não foi possível criar o link.");

  const { id } = await response.json();
  const fragment = `${id}.${encodeBase64Url(keyBytes)}`;
  const url = `${location.origin}/#${fragment}`;
  document.querySelector("#share-link").value = url;
  document.querySelector("#secret").value = "";
  createView.hidden = true;
  resultView.hidden = false;
  status.textContent = "Link criado. A chave de descriptografia está apenas no fragmento da URL.";
}

async function openLink() {
  const [id, encodedKey, extra] = location.hash.slice(1).split(".");
  if (!id || !encodedKey || extra) return;

  createView.hidden = true;
  status.textContent = "Buscando e descriptografando o segredo…";
  let claimToken = "";
  try {
    const response = await fetch(`/api/secrets/${encodeURIComponent(id)}`, { cache: "no-store" });
    if (response.status === 409) throw new Error("Este segredo já está sendo aberto. Tente novamente em instantes.");
    if (!response.ok) throw new Error("Este link já foi usado, expirou ou não existe.");
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
      throw new Error("Não foi possível descriptografar. O segredo continua disponível para tentar novamente.");
    }

    const acknowledgement = await fetch(`/api/secrets/${encodeURIComponent(id)}/ack`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token: claimToken, proof: payload.proof }),
      cache: "no-store",
    });
    if (!acknowledgement.ok) {
      throw new Error("O segredo foi descriptografado, mas não foi possível confirmar a leitura. Reabra o link em instantes.");
    }

    history.replaceState(null, "", location.pathname);
    document.querySelector("#opened-secret").textContent = payload.secret;
    openView.hidden = false;
    status.textContent = "";
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
    status.textContent = error.message || "Não foi possível descriptografar. O link continua disponível para tentar novamente.";
  }
}

document.querySelector("#create-button").addEventListener("click", () => {
  createLink().catch((error) => { status.textContent = error.message; });
});
document.querySelector("#copy-button").addEventListener("click", async () => {
  await navigator.clipboard.writeText(document.querySelector("#share-link").value);
  status.textContent = "Link copiado.";
});
openLink();
