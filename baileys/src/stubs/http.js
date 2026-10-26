const na = () => { throw new Error('http is not available in the browser; use fetch'); };
export const request = na, get = na;
export class Agent {}
export default { request, get, Agent };
