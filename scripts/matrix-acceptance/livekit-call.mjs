// node livekit-call.mjs WS_ORIGIN JWT: stays in a LiveKit call as the grant's participant, as a
// client would: LiveKit's signalling WebSocket, pinged every 5 seconds (SignalRequest.ping,
// field 14). No media: the step only needs a participant that LiveKit keeps until it goes away.
// Prints "joined" on LiveKit's first answer and "closed <code>" when the connection ends.
const [origin, jwt] = process.argv.slice(2);
const ws = new WebSocket(`${origin}/rtc?access_token=${encodeURIComponent(jwt)}&protocol=15&sdk=js&auto_subscribe=0`);
const varint = (v) => {
	const b = [];
	for (; v > 127n; v >>= 7n) b.push(Number(v & 127n) | 128);
	return [...b, Number(v)];
};
let joined = false;
ws.onopen = () => setInterval(() => ws.send(new Uint8Array([0x70, ...varint(BigInt(Date.now()))])), 5000);
ws.onmessage = () => {
	if (!joined) console.log('joined');
	joined = true;
};
ws.onclose = (e) => {
	console.log(`closed ${e.code}`);
	process.exit(0);
};
ws.onerror = () => {};
