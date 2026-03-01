import httpClient from '../dataProvider/httpClient'

const bluetoothClient = {
  list: () => httpClient('/api/bluetooth/devices').then(({ json }) => json),

  scan: () =>
    httpClient('/api/bluetooth/scan', {
      method: 'POST',
    }).then(({ json }) => json),

  connect: (mac) =>
    httpClient('/api/bluetooth/connect', {
      method: 'POST',
      body: JSON.stringify({ mac }),
    }).then(({ json }) => json),

  disconnect: (mac) =>
    httpClient('/api/bluetooth/disconnect', {
      method: 'POST',
      body: JSON.stringify({ mac }),
    }).then(({ json }) => json),
}

export default bluetoothClient
