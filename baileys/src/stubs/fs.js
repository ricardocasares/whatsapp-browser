const notAvailable = () => { throw new Error('fs is not available in the browser'); };
export const createReadStream = notAvailable;
export const createWriteStream = notAvailable;
export class WriteStream {}
export const promises = { readFile: notAvailable, writeFile: notAvailable, unlink: notAvailable, stat: notAvailable, mkdir: notAvailable, access: notAvailable };
export const mkdir = notAvailable, readFile = notAvailable, stat = notAvailable, unlink = notAvailable, writeFile = notAvailable;
export default { createReadStream, createWriteStream, WriteStream, promises };
