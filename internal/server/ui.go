package server

const safeIndexHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Stream archive</title><link rel="stylesheet" href="/static/app.css"><script defer src="/static/hls.min.js"></script><script defer src="/static/app.js"></script></head><body>
<h1>Stream archive</h1>
<form id="start"><label>Adapter <select id="adapter" required></select></label> <label>Title <input name="title"></label><fieldset><legend>Recording input</legend><div id="input-fields"></div></fieldset><button type="submit">Start recording</button></form>
<section id="configuration-panel"><h2>Adapter settings</h2><div id="config-fields"></div><button id="save-config" type="button">Save Settings</button></section>
<section id="challenge-panel" hidden><h2>Continue adapter workflow</h2><div id="challenge-message"></div><div id="challenge-fields"></div><button id="continue-workflow" type="button">Continue</button> <button id="cancel-workflow" type="button">Cancel workflow</button></section>
<p id="message" role="status"></p><ul id="recordings"></ul><video id="player" controls playsinline></video>
</body></html>`
