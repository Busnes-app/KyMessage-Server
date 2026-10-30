import React, { useRef } from 'react';
import mitLicenseText from '../mit.txt?raw';

// The suite's licence line and About dialog, as in KyPost.
export const LicenseFooter: React.FC<{ appVersion?: string }> = ({ appVersion }) => {
  const dialog = useRef<HTMLDialogElement>(null);
  const year = new Date().getFullYear();
  const close = () => dialog.current?.close();

  return (
    <div className="sidebar-footer">
      <p>
        <button type="button" className="license-link" onClick={() => dialog.current?.showModal()}>
          &copy; {year} &middot; MIT License
        </button>
      </p>
      <dialog
        ref={dialog}
        className="modal-window license-window"
        aria-labelledby="license-app-name"
      >
        <div className="license-window-header">
          <div className="license-window-title">
            <div className="license-title-main">
              <span id="license-app-name" className="license-app-name">KyMessages</span>
              {appVersion ? <span className="license-version-badge">v{appVersion}</span> : null}
            </div>
            <p className="license-title-sub">Developed by Busnes.app</p>
            <p className="license-title-sub">&copy; {year} &middot; Licensed under the MIT License</p>
          </div>
          <button type="button" className="btn-ghost" onClick={close}>Close</button>
        </div>
        <textarea className="license-text" readOnly value={mitLicenseText} aria-label="MIT License" />
      </dialog>
    </div>
  );
};
