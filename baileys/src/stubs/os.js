export const platform = () => 'browser';
export const release = () => navigator.userAgent;
export const tmpdir = () => '/tmp';
export default { platform, release, tmpdir };
