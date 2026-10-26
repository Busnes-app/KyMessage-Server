// Users, Agents and Rooms with Matrix off: what is missing and where setup starts.
export function ChatNotSetUp() {
  return (
    <div className="dr-empty">
      <p role="status">Chat (Matrix) is not set up on this server.</p>
      <p className="dr-hint">Set it up with <code>matrix-init</code> and the Matrix Compose overlay, as docs/CHAT-SETUP.md describes.</p>
    </div>
  );
}
