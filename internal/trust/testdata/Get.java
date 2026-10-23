// Get asks each URL with Java's own HTTPS client and prints its status:
// the JVM trusts what its trust store holds, nothing else.
public class Get {
    public static void main(String[] urls) throws Exception {
        for (String url : urls) {
            var c = (javax.net.ssl.HttpsURLConnection) java.net.URI.create(url).toURL().openConnection();
            System.out.println(url + " " + c.getResponseCode());
        }
    }
}
