// Members chat in Element (Matrix sub-project 2); the console is for administrators.
export function MemberHome({user, onLogout}: {user: {display_name?: string; username: string}; onLogout: () => void}) {
  return <section className="card" aria-labelledby="member-home">
    <h2 id="member-home">{user.display_name || user.username}</h2>
    <p>Signed in as {user.username}.</p>
    <p>Chat isn't available yet.</p>
    <button type="button" className="btn-secondary" onClick={onLogout}>Sign out</button>
  </section>;
}
