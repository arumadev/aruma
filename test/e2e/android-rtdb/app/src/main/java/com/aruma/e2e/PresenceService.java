package com.aruma.e2e;

import android.app.Service;
import android.content.Intent;
import android.content.pm.ApplicationInfo;
import android.content.pm.PackageManager;
import android.os.Binder;
import android.os.Bundle;
import android.os.IBinder;
import android.os.Process;
import android.os.ResultReceiver;

import com.google.firebase.FirebaseApp;
import com.google.firebase.auth.FirebaseAuth;
import com.google.firebase.database.DatabaseReference;
import com.google.firebase.database.FirebaseDatabase;
import com.google.firebase.database.ServerValue;

import dev.aruma.redirect.ArumaRedirect;

import java.util.HashMap;
import java.util.Map;

/**
 * A second database client, in a process of its own (":presence" in the manifest) that the
 * presence test kills: it signs in anonymously, registers an onDisconnect that marks it offline,
 * marks itself online, and replies with its uid and pid through the ResultReceiver of the bind
 * intent ("reply"). Its database connection dies with the process, as an app's does when the
 * system kills it.
 */
public class PresenceService extends Service {
    @Override
    public IBinder onBind(Intent intent) {
        ResultReceiver reply = intent.getParcelableExtra("reply");
        try {
            start(reply);
        } catch (Exception e) {
            fail(reply, e);
        }
        return new Binder();
    }

    private void start(ResultReceiver reply) throws Exception {
        // Content providers run in the main process only: neither aruma-redirect's nor Firebase's
        // own initialises this one, so it does what they do.
        ApplicationInfo info = getPackageManager().getApplicationInfo(getPackageName(), PackageManager.GET_META_DATA);
        ArumaRedirect.install(info.metaData.getString(ArumaRedirect.META_URL));
        if (FirebaseApp.getApps(this).isEmpty()) {
            FirebaseApp.initializeApp(this);
        }
        FirebaseAuth.getInstance().signInAnonymously()
                .continueWithTask(t -> {
                    String uid = t.getResult().getUser().getUid();
                    DatabaseReference ref = FirebaseDatabase.getInstance().getReference("status").child(uid);
                    return ref.onDisconnect().setValue(state("offline"))
                            .continueWithTask(d -> {
                                d.getResult();
                                return ref.setValue(state("online"));
                            })
                            .continueWith(s -> {
                                s.getResult();
                                return uid;
                            });
                })
                .addOnCompleteListener(t -> {
                    if (!t.isSuccessful()) {
                        fail(reply, t.getException());
                        return;
                    }
                    Bundle b = new Bundle();
                    b.putString("uid", t.getResult());
                    b.putInt("pid", Process.myPid());
                    reply.send(0, b);
                });
    }

    private static Map<String, Object> state(String state) {
        Map<String, Object> m = new HashMap<>();
        m.put("state", state);
        m.put("at", ServerValue.TIMESTAMP);
        return m;
    }

    private static void fail(ResultReceiver reply, Exception e) {
        Bundle b = new Bundle();
        b.putString("error", String.valueOf(e));
        reply.send(1, b);
    }
}
