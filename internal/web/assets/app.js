import { createApp } from "./application.mjs";

await createApp();

const logout = document.getElementById("access-logout");
if (logout) {
  fetch("/v1/access/session", {cache:"no-store"}).then(response => {logout.hidden = !response.ok;}).catch(() => {});
  logout.addEventListener("click", async () => {
    const response = await fetch("/v1/access/logout", {method:"POST", headers:{"Content-Type":"application/json"}, body:"{}"});
    if (response.ok) location.assign("/access");
  });
}
