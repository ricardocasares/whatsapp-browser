export const exec = (_cmd, cb) => cb?.(new Error('child_process is not available in the browser'));
export default { exec };
