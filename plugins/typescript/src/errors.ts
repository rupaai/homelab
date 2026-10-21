/** The one error type thrown by the SDK. */
export class HomelabError extends Error {
  override readonly name = "HomelabError";
  /** HTTP status, or 0 for network errors and timeouts. */
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}
