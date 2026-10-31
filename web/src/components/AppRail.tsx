import React from 'react';
import { appMark } from '../ky-ui/appmarks.js';
import type { SuiteApp } from '../dto';

/** This console's KyIdentity client ID. */
export const selfClientID = 'kymessage';

/** ky-ui's app rail: the other suite apps, plain same-tab links. Nothing when the list is empty. */
export const AppRail: React.FC<{ apps: SuiteApp[] }> = ({ apps }) => {
  if (apps.length === 0) return null;
  return (
    <nav className="ky-app-rail" aria-label="Apps">
      <p className="ky-app-rail-label" aria-hidden="true">Apps</p>
      <ul>
        {apps.map((a) => (
          <li key={a.id}>
            <a href={a.url} title={a.name} aria-label={a.name} aria-current={a.id === selfClientID ? 'page' : undefined}>
              <img src={appMark(a.id)} width={24} height={24} alt="" />
            </a>
          </li>
        ))}
      </ul>
    </nav>
  );
};
